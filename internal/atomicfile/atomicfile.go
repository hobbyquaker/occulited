// Package atomicfile writes a file through <path>.tmp and a rename, and leaves no .tmp behind
// when either step fails (occulited B-99: on a full user partition the nightly backup's
// created.json was not written and an empty created.json.tmp stayed).
package atomicfile

import "os"

// Write writes b to path+".tmp" with mode and renames it over path. On an error the .tmp is
// removed and path is as it was. The directory must exist.
func Write(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	// a .tmp an earlier crash left keeps its own mode through os.WriteFile: it goes first
	_ = os.Remove(tmp)
	err := writeFile(tmp, b, mode)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// writeFile is os.WriteFile (a test swaps it for a full disk).
var writeFile = os.WriteFile
