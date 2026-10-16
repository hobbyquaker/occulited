package system

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/priv"
)

// HDMI is the Hardware page's HDMI switch (openccu-lite task 340): the HDMI output of the
// Raspberry Pi products, off unless the user switches it on. It is switched at runtime, never in
// config.txt: the boot and the recovery system stay visible on a monitor, and switching needs no
// reboot. Off is applied once the boot has finished (/var/status/startupFinished) and again at
// every boot; on gives the output back at once. The setting is occulited.json's hardware.hdmi
// (Get and Save); the privilege helper writes the connectors' sysfs status (priv.SetHDMI).
type HDMI struct {
	Root Root
	// Get reads the setting, Save writes it.
	Get  func() bool
	Save func(on bool) error
	// Set is the helper's switch; nil = Priv.SetHDMI.
	Set func(ctx context.Context, on bool) error

	mu      sync.Mutex
	applied bool   // the boot's off has been written (or nothing had to be)
	lastErr string // the last switch's error, for the view
}

// HDMIConnector is one HDMI connector as the DRM class directory says it.
type HDMIConnector struct {
	Name string `json:"name"` // HDMI-A-1
	// Status is the sysfs status: connected, disconnected (also when switched off)
	Status string `json:"status"`
	// Enabled says the connector drives a CRTC; DPMS is On or Off
	Enabled bool   `json:"enabled"`
	DPMS    string `json:"dpms"`
}

// HDMIView is GET /hardware/hdmi.
type HDMIView struct {
	// Available: a Raspberry Pi product with at least one HDMI connector; the page shows the
	// section only then. Reason says why not: not-a-pi, no-connector.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Enabled is the setting: false (the default) switches the output off.
	Enabled    bool            `json:"enabled"`
	Connectors []HDMIConnector `json:"connectors"`
	// Pending: the setting is off but the boot has not finished, so the output is still on.
	Pending bool   `json:"pending,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ErrHDMIUnavailable is Switch's answer on a system without the HDMI section.
var ErrHDMIUnavailable = errors.New("this system has no HDMI output to switch")

// hdmiPlatform: the Raspberry Pi products, the CCU3 included (it runs the rpi3 image).
func hdmiPlatform(v Version) bool {
	return v.Platform == "rpi3" || v.Platform == "rpi4" || strings.HasPrefix(v.Product, "rpi") || v.Product == "ccu3"
}

// HDMIConnectors reads the HDMI connectors' state under /sys/class/drm.
func (r Root) HDMIConnectors() []HDMIConnector {
	dir := r.join("/sys/class/drm")
	out := []HDMIConnector{}
	for _, c := range priv.HDMIConnectors(dir) {
		read := func(attr string) string { return strings.TrimSpace(readFile(filepath.Join(dir, c, attr))) }
		out = append(out, HDMIConnector{Name: c[strings.Index(c, "-")+1:], Status: read("status"), Enabled: read("enabled") == "enabled", DPMS: read("dpms")})
	}
	return out
}

func (h *HDMI) available() (bool, string) {
	if !hdmiPlatform(h.Root.ReadVersion()) {
		return false, "not-a-pi"
	}
	if len(priv.HDMIConnectors(h.Root.join("/sys/class/drm"))) == 0 {
		return false, "no-connector"
	}
	return true, ""
}

func (h *HDMI) set(ctx context.Context, on bool) error {
	if h.Set != nil {
		return h.Set(ctx, on)
	}
	return Priv.SetHDMI(ctx, on)
}

// View is the setting and the connectors' state.
func (h *HDMI) View() HDMIView {
	v := HDMIView{Enabled: h.Get(), Connectors: []HDMIConnector{}}
	v.Available, v.Reason = h.available()
	if v.Available {
		v.Connectors = h.Root.HDMIConnectors()
	}
	h.mu.Lock()
	v.Pending = v.Available && !v.Enabled && !h.applied
	v.Error = h.lastErr
	h.mu.Unlock()
	return v
}

// Switch saves the setting and applies it at once. Off before the boot has finished is saved and
// applied when it has (Pending), as at every boot.
func (h *HDMI) Switch(ctx context.Context, on bool) (HDMIView, error) {
	if ok, _ := h.available(); !ok {
		return h.View(), ErrHDMIUnavailable
	}
	if err := h.Save(on); err != nil {
		return h.View(), err
	}
	h.mu.Lock()
	apply := on || h.applied
	h.mu.Unlock()
	if apply {
		err := h.set(ctx, on)
		h.mu.Lock()
		h.lastErr = ""
		if err != nil {
			h.lastErr = err.Error()
		}
		h.mu.Unlock()
		if err != nil {
			return h.View(), err
		}
		h.settle(ctx, on)
	}
	return h.View(), nil
}

// hdmiSettle is how long Switch waits for the kernel's modeset after the write, so the answer
// shows the connectors as they end up rather than in between; a variable for the tests.
var hdmiSettle = 1500 * time.Millisecond

// settle waits until no HDMI connector drives a display pipe (off) or one does (on: a connected
// one, measured at 100-500 ms on rpi4), or hdmiSettle has passed - with no monitor and no forced
// connector nothing is ever enabled, so the wait is bounded.
func (h *HDMI) settle(ctx context.Context, on bool) {
	deadline := time.Now().Add(hdmiSettle)
	for time.Now().Before(deadline) {
		driving := false
		for _, c := range h.Root.HDMIConnectors() {
			driving = driving || c.Enabled
		}
		if driving == on {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// hdmiPoll is how often ApplyAtBoot looks for the finished boot; a variable for the tests.
var hdmiPoll = 5 * time.Second

// hdmiBootLimit is the uptime after which the boot counts as finished without the marker.
const hdmiBootLimit = 15 * time.Minute

// ApplyAtBoot waits for the finished boot and then switches the output off when the setting says
// so. Run once, in its own goroutine, at occulited's start; an occulited restarted later finds the
// boot finished and applies at once. With the setting on nothing is written: the kernel's own
// state is on.
func (h *HDMI) ApplyAtBoot(ctx context.Context) {
	if ok, _ := h.available(); !ok {
		h.mu.Lock()
		h.applied = true
		h.mu.Unlock()
		return
	}
	marker := h.Root.join("/var/status/startupFinished")
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		// the LED controller's backstop: after this long the system counts as up whatever the
		// files say (a boot whose last step failed writes no marker)
		if up, ok := h.Root.Uptime(); ok && up >= hdmiBootLimit {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(hdmiPoll):
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.applied = true
	if h.Get() {
		return
	}
	if err := h.set(ctx, false); err != nil {
		h.lastErr = err.Error()
		slog.Warn("hdmi: switching the output off failed", "err", err)
		return
	}
	slog.Info("hdmi: the output is switched off (the Hardware page switches it on)")
}
