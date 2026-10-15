package firmware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// occulited task 48: downloaded, not delivered - the global switch off by default on every system,
// the delivery of a held bundle over an older offered one, and the held directory's names.

func TestAutoDeployOffByDefaultAndOnceOnUpdate(t *testing.T) {
	for _, c := range []struct {
		name, state string
		want        bool
	}{
		{"no state file", "", false},
		{"task 45 default (no key)", `{"last_result": []}`, false},
		// task 45's explicit "on" is switched off once by the update: its key is no longer read
		{"task 45 on", `{"auto_deploy_off": false}`, false},
		{"task 45 off", `{"auto_deploy_off": true}`, false},
		{"switched on after task 48", `{"auto_deploy": true}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if c.state != "" {
				if err := os.WriteFile(path, []byte(c.state), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s := NewService(New("http://127.0.0.1:1"), t.TempDir(), func() []interfaces.Interface { return nil }, nil)
			s.Open(path)
			if got := s.Status().AutoDeploy; got != c.want {
				t.Errorf("auto_deploy %v, want %v", got, c.want)
			}
		})
	}
	// on, kept across a restart
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	s := NewService(New("http://127.0.0.1:1"), root, func() []interfaces.Interface { return nil }, nil)
	s.Open(path)
	s.SetAutoDeploy(true)
	s2 := NewService(New("http://127.0.0.1:1"), root, func() []interfaces.Interface { return nil }, nil)
	s2.Open(path)
	if !s2.AutoDeploy() {
		t.Error("on is not kept")
	}
}

// A newer download of a delivered type lies in the held directory beside the offered older one;
// delivering it moves it into the offered one's directory and the older one into the held
// directory - nothing is deleted, and the API's directory name is the held one.
func TestDeliverAHeldBundleOverTheOfferedOne(t *testing.T) {
	rc := newRefreshCounter(t)
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	held := filepath.Join(root, HeldDirName)
	writeHeldTestBundle(t, dir, "332", "HmIP-BBL", "332", "1.10.10")
	writeHeldTestBundle(t, held, "332-HmIP-BBL-1.10.16", "HmIP-BBL", "332", "1.10.16")
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	b, err := s.SetTransfer("332-HmIP-BBL-1.10.16", true)
	if err != nil || b.Held || b.Dir != "332" || b.Version != "1.10.16" {
		t.Fatalf("deliver: %+v %v", b, err)
	}
	if got := bundleVersionAt(t, filepath.Join(dir, "332")); got != "1.10.16" {
		t.Errorf("offered: %s", got)
	}
	if got := bundleVersionAt(t, filepath.Join(held, "332-HmIP-BBL-1.10.10")); got != "1.10.10" {
		t.Errorf("the older one is not held: %s", got)
	}
	if _, err := os.Stat(filepath.Join(held, "332-HmIP-BBL-1.10.16")); !os.IsNotExist(err) {
		t.Errorf("the delivered one is still held: %v", err)
	}
	if rc.count() != 1 {
		t.Errorf("refreshes: %d", rc.count())
	}
	// the offered directory name delivers nothing new, it only clears the mark
	if b, err := s.SetTransfer("332", true); err != nil || b.Version != "1.10.16" || b.Held {
		t.Errorf("on again: %+v %v", b, err)
	}
}

// A held bundle whose TypeCode another type's offered bundle holds is delivered into a directory
// of its own (B-225's shape), never over the other type's.
func TestDeliverNeverReplacesAnotherTypesBundle(t *testing.T) {
	rc := newRefreshCounter(t)
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	held := filepath.Join(root, HeldDirName)
	writeHeldTestBundle(t, dir, "261", "HM-LC-Dim1T-DR", "261", "1.1.0")
	// held before task 48: the bare TypeCode directory
	writeHeldTestBundle(t, held, "261", "HmIP-WRC2", "261", "1.18.2")
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return ifs(rc.srv.URL) }, nil)
	b, err := s.SetTransfer("261", true)
	if err != nil || b.Dir != "261-HmIP-WRC2" {
		t.Fatalf("deliver: %+v %v", b, err)
	}
	if got := bundleNameAt(t, filepath.Join(dir, "261")); got != "HM-LC-Dim1T-DR" {
		t.Errorf("261 holds %s", got)
	}
	if got := bundleNameAt(t, filepath.Join(dir, "261-HmIP-WRC2")); got != "HmIP-WRC2" {
		t.Errorf("261-HmIP-WRC2 holds %s", got)
	}
	// and back: held under its type and version
	b, err = s.SetTransfer("261-HmIP-WRC2", false)
	if err != nil || !b.Held || b.Dir != "261-HmIP-WRC2-1.18.2" {
		t.Fatalf("hold: %+v %v", b, err)
	}
	if got := bundleNameAt(t, filepath.Join(dir, "261")); got != "HM-LC-Dim1T-DR" {
		t.Errorf("after the hold 261 holds %s", got)
	}
}

// pruneHeld keeps the type's newest held bundle and the previously delivered one and leaves other
// types alone; a held bundle's text files are readable through BundleFile.
func TestPruneHeldAndHeldBundleFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "firmware")
	held := filepath.Join(root, HeldDirName)
	writeHeldTestBundle(t, held, "332", "HmIP-BBL", "332", "1.10.10")
	writeHeldTestBundle(t, held, "332-HmIP-BBL-1.10.16", "HmIP-BBL", "332", "1.10.16")
	writeHeldTestBundle(t, held, "332-HmIP-BBL-1.10.20", "HmIP-BBL", "332", "1.10.20")
	writeHeldTestBundle(t, held, "261-HmIP-WRC2-1.18.0", "HmIP-WRC2", "261", "1.18.0")
	if err := os.WriteFile(filepath.Join(held, "332-HmIP-BBL-1.10.16", "changelog.txt"), []byte("fixes"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewService(New("http://127.0.0.1:1"), dir, func() []interfaces.Interface { return nil }, nil)
	if raw, err := s.BundleFile("332-HmIP-BBL-1.10.16", "changelog.txt"); err != nil || string(raw) != "fixes" {
		t.Errorf("held changelog: %q %v", raw, err)
	}
	if _, err := s.BundleFile("332-HmIP-BBL-1.10.16", "../info"); err == nil {
		t.Error("a path was read")
	}
	// task 49: the newest held and the previously delivered version stay
	s.prev = map[string]string{"hmip-bbl": "1.10.10"}
	s.pruneHeld("hmip-bbl")
	for d, want := range map[string]bool{"332": true, "332-HmIP-BBL-1.10.16": false, "332-HmIP-BBL-1.10.20": true, "261-HmIP-WRC2-1.18.0": true} {
		if _, err := os.Stat(filepath.Join(held, d)); (err == nil) != want {
			t.Errorf("%s: kept %v, want %v", d, err == nil, want)
		}
	}
	if got := heldName(&Bundle{TypeCode: "310", Name: "HmIP-PDT", VersionFromName: "2.2.4"}); got != "310-HmIP-PDT-2.2.4" {
		t.Errorf("heldName from the file name: %s", got)
	}
	if got := heldName(&Bundle{TypeCode: "1", Name: "a b/c"}); got != "1-a_b_c-0" {
		t.Errorf("heldName unsafe: %s", got)
	}
}

func bundleVersionAt(t *testing.T, path string) string {
	t.Helper()
	b, _ := bundleAt(path, filepath.Base(path))
	if b == nil {
		return ""
	}
	return b.Version
}

func bundleNameAt(t *testing.T, path string) string {
	t.Helper()
	b, _ := bundleAt(path, filepath.Base(path))
	if b == nil {
		return ""
	}
	return b.Name
}
