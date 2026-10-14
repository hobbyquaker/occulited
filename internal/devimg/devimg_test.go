package devimg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// the shape of the catalogue's DEV_PATHS line, as OpenCCU's DEVDB.tcl writes it
const sample = `#!/bin/tclsh
set DEV_LIST {HmIP-WRC2 HM-CC-TC {ZEL STG RM FWT}}
array set DEV_DESCRIPTION {HmIP-WRC2 HmIP-WRC2 HM-CC-TC HM-CC-TC}
array set DEV_PATHS       {HmIP-WRC2 {{50 /config/img/devices/50/112_hmip-wrc2_thumb.png} {250 /config/img/devices/250/112_hmip-wrc2.png}} {ZEL STG RM FWT} {{50 /config/img/devices/50/42_hm-cc-tc_thumb.png} {250 /config/img/devices/250/42_hm-cc-tc.png}} HM-CC-TC {{50 /config/img/devices/50/42_hm-cc-tc_thumb.png} {250 /config/img/devices/250/42_hm-cc-tc.png}} EVIL {{50 /config/img/devices/50/../../x.png}} OTHER {{50 /etc/passwd}}}
array set DEV_HIGHLIGHT   {HmIP-WRC2 {{"2" 4 0.540 0.366 0.04 0.044}}}
`

func TestParse(t *testing.T) {
	m := Parse(sample)
	if len(m) != 3 || m["HmIP-WRC2"] != "/config/img/devices/50/112_hmip-wrc2_thumb.png" || m["ZEL STG RM FWT"] != "/config/img/devices/50/42_hm-cc-tc_thumb.png" {
		t.Fatalf("%v", m)
	}
	for _, bad := range []string{"", "array set DEV_PATHS", "array set DEV_PATHS {HmIP-WRC2 {{50 /x}"} {
		if len(Parse(bad)) != 0 {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestCatalog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "DEVDB.tcl")
	c := &Catalog{Path: p}
	if c.Image("HmIP-WRC2") != "" {
		t.Fatal("a picture without the file")
	}
	if err := os.WriteFile(p, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	if c.Image("HmIP-WRC2") == "" || c.Image("HMIP-WRC2") != c.Image("HmIP-WRC2") || c.Image("") != "" || c.Image("HmIP-NONE") != "" {
		t.Fatalf("lookups: %q %q", c.Image("HmIP-WRC2"), c.Image("HMIP-WRC2"))
	}
	// a changed file is read again
	_ = os.WriteFile(p, []byte(`array set DEV_PATHS {HmIP-PDT {{50 /config/img/devices/50/pdt.png}}}`), 0o644)
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(p, later, later)
	if c.Image("HmIP-PDT") != "/config/img/devices/50/pdt.png" || c.Image("HmIP-WRC2") != "" {
		t.Fatal("the changed catalogue was not read")
	}
	if (&Catalog{}).Image("x") != "" && os.Getenv("CI") == "" {
		t.Log("the default path exists on this machine")
	}
}
