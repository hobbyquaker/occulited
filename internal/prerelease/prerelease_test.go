package prerelease

import "testing"

// openccu-lite task 291: dev.31 is the last dev release and must find beta.0 in the update check.
func TestCompareOrder(t *testing.T) {
	// each one after the one before it
	order := []string{
		"", // an empty identifier is text, not a number: an unknown tag
		"0-snapshot",
		"SNAPSHOT",
		"nightly",
		"dev",
		"dev.1",
		"dev.30",
		"dev.31",
		"alpha",
		"alpha.0",
		"alpha.1",
		"beta.0",
		"beta.1",
		"beta.2",
		"beta.10",
		"beta.10.1",
		"beta.11",
		"rc.1",
		"rc.2",
		"rc.10",
	}
	for i := range order {
		for j := range order {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(order[i], order[j]); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestCompareIdentifiers(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1", "beta", -1}, // a number before text
		{"beta", "1", 1},
		{"2", "10", -1}, // numerically
		{"007", "7", 0}, // leading zeros do not count
		{"99999999999999999999", "100000000000000000000", -1}, // any length
		{"Beta", "dev", -1},       // case matters: an unknown tag
		{"foo", "bar", 1},         // unknown tags in ASCII order
		{"rc1", "rc", -1},         // "rc1" is not the tag rc
		{"beta.x", "beta.1", 1},   // text after a number in the same place
		{"dev.99", "alpha.0", -1}, // the tag decides before the number
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	order := []string{"1.0.0-dev.31", "1.0.0-alpha.0", "1.0.0-beta.10", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0-dev.6", "1.1.0-dev.10", "1.1.0", "2.0.0"}
	for i, a := range order {
		for j, b := range order {
			c, ok := CompareVersions(a, b)
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if !ok || c != want {
				t.Errorf("CompareVersions(%s, %s) = %d, %v; want %d", a, b, c, ok, want)
			}
		}
	}
	if c, ok := CompareVersions("1.2.3+4", "1.2.3"); !ok || c != 0 {
		t.Errorf("build metadata is not compared: %d, %v", c, ok)
	}
	for _, bad := range []string{"", "0-beta.2", "1.2", "x"} {
		if _, ok := CompareVersions(bad, "1.0.0"); ok {
			t.Errorf("%q is no semantic version", bad)
		}
	}
}
