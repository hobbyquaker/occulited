package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStageFileCallersBounded walks every stageFile call site in the package (B-256, the upload
// counterpart of httpapi's TestNoUnboundedBody) and requires the reader argument to be bounded on
// the spot: io.LimitReader or newCapReader (B-141: the cap that fails instead of cutting), or a small in-memory reader (strings/bytes). stageFile copies until EOF,
// so an unbounded reader lets one request fill the userfs; every caller must cap its source.
func TestStageFileCallersBounded(t *testing.T) {
	bounded := []string{"io.LimitReader(", "newCapReader(", "strings.NewReader(", "bytes.NewReader("}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "staging.go" {
			continue // staging.go defines stageFile and its bounded wrapper StageUpload
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "stageFile(") {
				continue
			}
			seen++
			ok := false
			for _, tok := range bounded {
				if strings.Contains(line, tok) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s:%d stageFile with an unbounded reader; wrap it in io.LimitReader: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
	if seen < 3 {
		t.Errorf("found only %d stageFile call sites; the walker matched too little", seen)
	}
}
