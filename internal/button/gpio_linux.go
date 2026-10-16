//go:build linux

package button

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
	"unsafe"
)

// The GPIO character device's ABI v2 (linux/gpio.h), pure Go: no cgo, no libgpiod (D-15). The
// structures are written and read as little-endian bytes at the uapi's offsets - every target
// (amd64, arm64, armv7) is little-endian - and the sizes are the uapi's on every architecture
// (its 64-bit fields are __aligned_u64): gpiochip_info 68, gpio_v2_line_info 256,
// gpio_v2_line_request 592, gpio_v2_line_event 48 bytes.
const (
	ioctlChipInfo  = 0x8044b401 // GPIO_GET_CHIPINFO_IOCTL
	ioctlLineInfo  = 0xc100b405 // GPIO_V2_GET_LINEINFO_IOCTL
	ioctlGetLine   = 0xc250b407 // GPIO_V2_GET_LINE_IOCTL
	ioctlGetValues = 0xc010b40e // GPIO_V2_LINE_GET_VALUES_IOCTL

	flagInput         = 1 << 2
	flagEdgeRising    = 1 << 4
	flagEdgeFalling   = 1 << 5
	flagBiasPullUp    = 1 << 8
	flagClockRealtime = 1 << 11
	attrDebounce      = 3

	eventRising  = 1
	eventFalling = 2

	sizeChipInfo = 68
	sizeLineInfo = 256
	sizeRequest  = 592
	sizeEvent    = 48
	sizeValues   = 16
)

// Edge is one transition of the line.
type Edge struct {
	// Low: the line went low - the button pressed (it is active low)
	Low bool
	At  time.Time
}

// Line is a requested input line.
type Line struct {
	Chip   string
	Offset uint32
	f      *os.File
}

func ioctl(fd uintptr, req uintptr, buf []byte) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return e
	}
	return nil
}

func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// FindLine looks for the line named name on the GPIO chips in dev (/dev/gpiochip*), so the header's
// chip is found whatever its number (the Pi 5's differs). ErrNoLine when no chip has it.
func FindLine(dev, name string) (chip string, offset uint32, err error) {
	chips, _ := filepath.Glob(filepath.Join(dev, "gpiochip*"))
	sort.Strings(chips)
	var errs []error
	for _, c := range chips {
		off, err := lineOffset(c, name)
		if err == nil {
			return c, off, nil
		}
		if !errors.Is(err, ErrNoLine) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return "", 0, fmt.Errorf("%w (%v)", ErrNoLine, errors.Join(errs...))
	}
	return "", 0, ErrNoLine
}

// ErrNoLine: no GPIO chip has a line of that name.
var ErrNoLine = errors.New("no GPIO line of that name")

func lineOffset(chip, name string) (uint32, error) {
	f, err := os.OpenFile(chip, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info := make([]byte, sizeChipInfo)
	if err := ioctl(f.Fd(), ioctlChipInfo, info); err != nil {
		return 0, fmt.Errorf("%s: chip info: %w", chip, err)
	}
	lines := binary.LittleEndian.Uint32(info[64:68])
	li := make([]byte, sizeLineInfo)
	for off := uint32(0); off < lines; off++ {
		clear(li)
		binary.LittleEndian.PutUint32(li[64:68], off)
		if err := ioctl(f.Fd(), ioctlLineInfo, li); err != nil {
			return 0, fmt.Errorf("%s: line %d: %w", chip, off, err)
		}
		if cstring(li[0:32]) == name {
			return off, nil
		}
	}
	return 0, ErrNoLine
}

// OpenLine requests one line as an input with the pull-up bias, both edges and the kernel's
// debounce, timestamps on the realtime clock.
func OpenLine(chip string, offset uint32, consumer string, debounce time.Duration) (*Line, error) {
	f, err := os.OpenFile(chip, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	req := make([]byte, sizeRequest)
	binary.LittleEndian.PutUint32(req[0:4], offset) // offsets[0]
	copy(req[256:256+31], consumer)                 // consumer[32]
	cfg := req[288 : 288+272]                       // config
	binary.LittleEndian.PutUint64(cfg[0:8], flagInput|flagEdgeRising|flagEdgeFalling|flagBiasPullUp|flagClockRealtime)
	if debounce > 0 {
		binary.LittleEndian.PutUint32(cfg[8:12], 1) // num_attrs
		a := cfg[32 : 32+24]                        // attrs[0]
		binary.LittleEndian.PutUint32(a[0:4], attrDebounce)
		binary.LittleEndian.PutUint32(a[8:12], uint32(debounce.Microseconds()))
		binary.LittleEndian.PutUint64(a[16:24], 1) // mask: line 0 of the request
	}
	binary.LittleEndian.PutUint32(req[560:564], 1) // num_lines
	if err := ioctl(f.Fd(), ioctlGetLine, req); err != nil {
		return nil, fmt.Errorf("%s: requesting line %d: %w", chip, offset, err)
	}
	fd := int(int32(binary.LittleEndian.Uint32(req[588:592])))
	if fd <= 0 {
		return nil, fmt.Errorf("%s: requesting line %d: no descriptor", chip, offset)
	}
	// non-blocking, so the runtime's poller waits for the events and Close ends a Read
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &Line{Chip: chip, Offset: offset, f: os.NewFile(uintptr(fd), fmt.Sprintf("%s:%d", chip, offset))}, nil
}

// High reads the line's level now: true released, false pressed.
func (l *Line) High() (bool, error) {
	v := make([]byte, sizeValues)
	binary.LittleEndian.PutUint64(v[8:16], 1) // mask
	rc, err := l.f.SyscallConn()
	if err != nil {
		return false, err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) { ierr = ioctl(fd, ioctlGetValues, v) }); err != nil {
		return false, err
	}
	if ierr != nil {
		return false, ierr
	}
	return binary.LittleEndian.Uint64(v[0:8])&1 == 1, nil
}

// Read waits for the next edge; it ends with an error when the line is closed.
func (l *Line) Read() (Edge, error) {
	buf := make([]byte, sizeEvent)
	for {
		n, err := l.f.Read(buf)
		if err != nil {
			return Edge{}, err
		}
		if n != sizeEvent {
			continue
		}
		ts := binary.LittleEndian.Uint64(buf[0:8])
		id := binary.LittleEndian.Uint32(buf[8:12])
		at := time.Unix(0, int64(ts))
		if ts == 0 {
			at = time.Now()
		}
		switch id {
		case eventFalling:
			return Edge{Low: true, At: at}, nil
		case eventRising:
			return Edge{Low: false, At: at}, nil
		}
	}
}

// Close releases the line.
func (l *Line) Close() error { return l.f.Close() }
