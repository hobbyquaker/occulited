package radio

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// hmipserver's known fatal errors (D-102): a start that cannot succeed is failed at once with its
// reason instead of waiting out the 300 s, and a marker keeps the unit from being restarted for it
// (hmipserver.service: ConditionPathExists=!/run/occulite/radio/hmipserver.fatal) until the next
// run of the radio stack - a re-detection, a hotplug that changes the plan, a connection change.

// HmIPFatal is the marker's content and the pages' warning.
type HmIPFatal struct {
	Code    string `json:"code"`
	Line    string `json:"line"`
	Adapter string `json:"adapter,omitempty"` // the SGTIN hmipserver was started on
	// Cause tells the two causes behind one rejection line apart: "unreachable" when the key
	// server was not reached at all, "refused" when it answered and did not hand the network to
	// this module (D-106). Empty for a marker written before the distinction existed.
	Cause string    `json:"cause,omitempty"`
	At    time.Time `json:"at"`
}

// The causes of an adapter-exchange rejection.
const (
	CauseUnreachable = "unreachable"
	CauseRefused     = "refused"
)

// FatalFile is the marker, under RunDir.
const FatalFile = "hmipserver.fatal"

// FatalNoModule: HMIPServer.jar has no HmIP module (occulited task 147). The prep step writes it
// when the plan has none; the pattern below catches a module whose node went between the plan and
// the start.
const FatalNoModule = "no-module"

var hmipFatalPatterns = []struct{ code, needle string }{
	// HMIPServer.jar could not open its module's port: it waits for it and never comes up
	{FatalNoModule, "LOCAL_ADAPTER_NO_SUCH_PORT"},
	// the box's HmIP identity belongs to another adapter, and eQ-3's key server does not hand it
	// to this one (a stick the key server does not know, on a box set up with another module)
	{"adapter-exchange-rejected", "Adapter exchange was rejected by key server"},
}

// keyServerUnreachable are the lines hmipserver writes before the rejection when eQ-3's key
// server was never reached: its own warning about an HTTP status other than 200, and the Java
// network exceptions of no DNS, no route, a refused or timed-out connection, a failed TLS
// handshake. Without one of them the server answered and refused the module (D-106: the
// diagnosis reads hmipserver's output and probes nothing).
var keyServerUnreachable = []string{
	"Frontend Server responded with HTTP Status",
	"java.net.UnknownHostException",
	"java.net.ConnectException",
	"java.net.NoRouteToHostException",
	"java.net.SocketTimeoutException",
	"java.net.SocketException",
	"javax.net.ssl.SSL",
}

// hmipFatal looks through the lines for a known fatal error. For an adapter-exchange rejection
// the cause is read from the lines before it.
func hmipFatal(text string) (code, line, cause string) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		for _, p := range hmipFatalPatterns {
			if strings.Contains(l, p.needle) {
				if p.code == "adapter-exchange-rejected" {
					cause = exchangeCause(lines[:i])
				}
				return p.code, strings.TrimSpace(l), cause
			}
		}
	}
	return "", "", ""
}

// exchangeCause is unreachable when one of the lines before the rejection says the key server was
// not reached, refused otherwise.
func exchangeCause(before []string) string {
	for _, l := range before {
		for _, n := range keyServerUnreachable {
			if strings.Contains(l, n) {
				return CauseUnreachable
			}
		}
	}
	return CauseRefused
}

// daemonOutput is what a daemon logged so far: the journal of its main process. hmipserver's JVM
// writes to the unit's stdout; multimacd logs through syslog, where journald takes the pid from
// the socket's credentials - _PID= finds both.
func daemonOutput(ctx context.Context, d Detector, mainPID int) string {
	if mainPID <= 0 {
		return ""
	}
	out, _ := d.run()(ctx, "journalctl", "_PID="+strconv.Itoa(mainPID), "-o", "cat", "--no-pager", "-q")
	return string(out)
}

// FatalPath is the marker's path under root: what clears it outside a run of the radio stack (the
// exchange retry and the fresh start) removes this file.
func FatalPath(root string) string { return shadowPath(root, FatalFile) }

// ReadHmIPFatal reads the marker; nil when there is none.
func ReadHmIPFatal(root string) *HmIPFatal {
	var f HmIPFatal
	if err := readJSONFile(shadowPath(root, FatalFile), &f); err != nil {
		return nil
	}
	return &f
}

// writeHmIPFatal writes the marker world-readable whatever the umask (openccu-lite B-284): the
// ready step runs inside hmipserver's unit, whose UMask=0077 is meant for what the server writes,
// and left the marker root's alone (0600) - the daemon, reading it as its own user, saw no
// rejection and the Interfaces page offered neither the retry nor the fresh start. The mode is set
// explicitly after the write, as the security counter state's is (task 299).
func writeHmIPFatal(root string, f HmIPFatal) {
	b, _ := json.MarshalIndent(f, "", "  ")
	p := shadowPath(root, FatalFile)
	_ = os.WriteFile(p, b, 0o644)
	_ = os.Chmod(p, 0o644)
}

// --- a declined inclusion (task 201) ---------------------------------------------------------

// InclusionDeclinedMatch is the fixed part of the line hmipserver writes when it turns an
// inclusion request down because the device's key in the map (task 154's sgtin.map) is not that
// device's key: "AP <sgtin>: The inclusion for device <sgtin> is declined, the local key is
// wrong". It is a plain substring, so journalctl can pre-filter on it (-g) before the regular
// expression below reads the device out.
//
// Task 201, decided in task 154's Q&A: in local key mode a device whose entry is wrong simply
// never appears - hmipserver declines and writes this, and nothing else says so. The match belongs
// here beside D-102's fatal-line matcher, not in a second reader of the same log.
const InclusionDeclinedMatch = "is declined, the local key is wrong"

// inclusionDeclinedRe reads the declined device out of such a line. The device is an SGTIN, the
// 24 hex digits the map is keyed by; the bounds are loose enough that a shorter or longer id would
// still be reported instead of the line being dropped.
var inclusionDeclinedRe = regexp.MustCompile(`The inclusion for device ([0-9A-Za-z]{6,32}) ` + regexp.QuoteMeta(InclusionDeclinedMatch))

// InclusionDeclinedSGTIN returns the declined device's SGTIN in upper case, or "" when the line is
// not such a decline. Only that one line matters: the neighbouring ones about the same map ("Add
// local key of device … to whitelist", "Invalid local key (size) …") are not a failed pairing.
func InclusionDeclinedSGTIN(line string) string {
	m := inclusionDeclinedRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}
