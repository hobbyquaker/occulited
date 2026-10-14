package system

import (
	"os"
	"runtime"
	"testing"
)

// occulited task 37: the system history's reading, and the CPU count the Status page reads the
// load against.
func TestReadUsage(t *testing.T) {
	r := fakeRoot(t)
	load, mem, ok := r.ReadUsage()
	if !ok || load != 0.52 || mem != 40 {
		t.Fatalf("%v %v %v", load, mem, ok)
	}
	if s := r.ReadStatus(); s.CPUs != runtime.NumCPU() || s.CPUs < 1 {
		t.Fatalf("cpus %d", s.CPUs)
	}
	// no MemAvailable: the memory is unknown, the load is not
	_ = os.WriteFile(r.join("/proc/meminfo"), []byte("MemTotal: 1000 kB\n"), 0o644)
	if load, mem, ok := r.ReadUsage(); !ok || load != 0.52 || mem != -1 {
		t.Fatalf("without MemAvailable: %v %v %v", load, mem, ok)
	}
	for _, bad := range []string{"", "x 1 2\n"} {
		_ = os.WriteFile(r.join("/proc/loadavg"), []byte(bad), 0o644)
		if _, _, ok := r.ReadUsage(); ok {
			t.Fatalf("%q read as a load", bad)
		}
	}
	_ = os.Remove(r.join("/proc/loadavg"))
	if _, _, ok := r.ReadUsage(); ok {
		t.Fatal("no /proc/loadavg read as a load")
	}
}
