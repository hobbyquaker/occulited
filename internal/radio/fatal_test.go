package radio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-106: the one rejection line hides two causes, told apart by what hmipserver wrote before it
func TestHmIPFatalExchangeCause(t *testing.T) {
	reject := "de.eq3.cbcs.server.local.base.internal.HMIPTRXInitialResponseListener [vert.x-eventloop-thread-0] Adapter exchange was rejected by key server.\n"
	cases := []struct {
		name, out, cause string
	}{
		{"answered and refused", "Init Hardware Info\nKeyServerCommunicationWorker request sent\n" + reject, CauseRefused},
		{"an HTTP status other than 200", "WARN Frontend Server responded with HTTP Status 503\n" + reject, CauseUnreachable},
		{"no DNS", "java.net.UnknownHostException: secgtw.homematic.com\n\tat java.base/java.net.InetAddress.getAllByName\n" + reject, CauseUnreachable},
		{"no connection", "java.net.ConnectException: Connection refused\n" + reject, CauseUnreachable},
		{"a timeout", "java.net.SocketTimeoutException: connect timed out\n" + reject, CauseUnreachable},
		{"a TLS failure", "javax.net.ssl.SSLHandshakeException: PKIX path building failed\n" + reject, CauseUnreachable},
		// a network line after the rejection belongs to something else
		{"the network line after it", reject + "java.net.ConnectException: later\n", CauseRefused},
	}
	for _, c := range cases {
		code, line, cause := hmipFatal(c.out)
		if code != "adapter-exchange-rejected" || line == "" || cause != c.cause {
			t.Errorf("%s: %q %q %q", c.name, code, line, cause)
		}
	}
	if code, _, cause := hmipFatal("java.net.ConnectException: nothing fatal\nstarted\n"); code != "" || cause != "" {
		t.Errorf("a network line alone is not fatal: %q %q", code, cause)
	}
}

// occulited task 147: HMIPServer.jar without its module never comes up; the prep step refuses the
// start with the no-module marker (no JVM, no fallback to HMServer.jar), and the JVM's own port
// error is the same code
func TestHmIPFatalNoModule(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(FatalPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	d := Detector{Root: root, Now: func() time.Time { return time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC) }}
	p := MakePlan(inputs())
	if !p.NoHmIPModule() {
		t.Fatalf("no module: %+v", p)
	}
	err := Prep(context.Background(), d, "hmipserver", p, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "set HmIP to none") {
		t.Fatalf("prep: %v", err)
	}
	if f := ReadHmIPFatal(root); f == nil || f.Code != FatalNoModule || f.Adapter != "" {
		t.Fatalf("marker: %+v", f)
	}
	in := inputs()
	in.HmIPUserConf = SetHmIPChoice("", "0000000FFF")
	if p = MakePlan(in); !p.NoHmIPModule() {
		t.Fatalf("pinned and missing: %+v", p)
	}
	_ = Prep(context.Background(), d, "hmipserver", p, func(string, ...any) {})
	if f := ReadHmIPFatal(root); f == nil || f.Adapter != "0000000FFF" {
		t.Fatalf("marker: %+v", f)
	}
	in.HmIPUserConf = SetHmIPChoice("", HmIPNone)
	if p = MakePlan(in); p.NoHmIPModule() {
		t.Fatalf("none: %+v", p)
	}
	code, _, _ := hmipFatal("Init Hardware Info\n[vert.x-eventloop-thread-3] Error 'LOCAL_ADAPTER_NO_SUCH_PORT' while trying to open port '/dev/raw-uart9':\n")
	if code != FatalNoModule {
		t.Fatalf("the JVM's port error: %q", code)
	}
}
