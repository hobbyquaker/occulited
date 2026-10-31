package system

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

func litePackage(t *testing.T, min string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	body := `{"format":1,"id":"hmm","name":"HMM","requires":{"lite":"` + min + `"}}`
	if err := tw.WriteHeader(&tar.Header{Name: manifest.FileName, Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// occulited task 134: a package for a newer system is refused before the installer runs.
func TestInstallRefusedBelowRequiresLite(t *testing.T) {
	rig := newSettleRig(t, failedStartUpdate)
	if err := os.WriteFile(rig.root.join("/VERSION"), []byte("VERSION=3.89.11\nLITE=1.1.0-dev.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rig.a.Install(context.Background(), bytes.NewReader(litePackage(t, "1.1.0-dev.6")))
	var e *manifest.LiteTooOldError
	if !errors.As(err, &e) || res != nil {
		t.Fatalf("res %v, err %v", res, err)
	}
	if _, serr := os.Stat(rig.root.join("/tmp/installer-ran")); serr == nil {
		t.Fatal("the installer ran")
	}
}
