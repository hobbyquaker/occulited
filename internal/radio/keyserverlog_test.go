package radio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B-63: in key-server mode the KeyServerWorker's start-up line about the missing local network key
// is filtered out - that one message only, at the level of the other loggers; in local key mode
// nothing is added, and a template that names the worker itself is left alone.
func TestQuietKeyServerWarning(t *testing.T) {
	tmpl := "<Configuration>\n\t<Loggers>\n\t\t<Logger name=\"de.eq3\" level=\"warn\"/>\n\t\t<Root level=\"warn\"/>\n\t</Loggers>\n</Configuration>\n"
	got := QuietKeyServerWarning(tmpl, "WARN", false)
	want := "\t\t<Logger name=\"de.eq3.cbcs.server.core.vertx.KeyServerWorker\" level=\"warn\">\n\t\t\t<RegexFilter regex=\".*Missing or invalid key server configuration parameter \\(Network\\.Key / Network\\.Key\\.Base\\).*\" onMatch=\"DENY\" onMismatch=\"NEUTRAL\"/>\n\t\t</Logger>\n\t\t<Logger name=\"de.eq3\""
	if !strings.Contains(got, want) {
		t.Fatalf("key-server mode:\n%s", got)
	}
	if QuietKeyServerWarning(tmpl, "WARN", true) != tmpl {
		t.Fatal("local key mode got the filter")
	}
	if QuietKeyServerWarning(got, "WARN", false) != got {
		t.Fatal("added twice")
	}
	// no de.eq3 logger: before </Loggers>
	bare := "<Loggers>\n<Root level=\"info\"/>\n</Loggers>\n"
	if g := QuietKeyServerWarning(bare, "INFO", false); !strings.Contains(g, "level=\"info\">") || strings.Index(g, "KeyServerWorker") > strings.Index(g, "</Loggers>") {
		t.Fatalf("bare:\n%s", g)
	}
	if QuietKeyServerWarning("<x/>", "WARN", false) != "<x/>" {
		t.Fatal("no Loggers element changed")
	}
}

func TestLocalNetworkKey(t *testing.T) {
	root := t.TempDir()
	d := Detector{Root: root}
	w := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	if localNetworkKey(d) {
		t.Fatal("nothing there")
	}
	w("var/etc/crRFD.conf", "KeyServer.Mode=KEYSERVER_LOCAL\nNetwork.Key=\n")
	if localNetworkKey(d) {
		t.Fatal("an empty key")
	}
	w("etc/config/crRFD/hmip_user.conf", "Network.Key=00112233445566778899AABBCCDDEEFF\n")
	if !localNetworkKey(d) {
		t.Fatal("the user's key")
	}
}
