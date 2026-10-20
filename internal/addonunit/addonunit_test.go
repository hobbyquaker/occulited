package addonunit

import (
	"strings"
	"testing"
)

func TestDropInValidate(t *testing.T) {
	good := []DropIn{
		{ID: "x", Mode: "root"},
		{ID: "x", Mode: "root", MayMount: true},
		{ID: "x.y-z", Mode: "confined", UID: MinUID, Groups: []string{"certs", "dialout"}, Capabilities: []string{"CAP_NET_BIND_SERVICE"}, Paths: []string{"/media", "/usr/local/x"}},
	}
	for _, d := range good {
		if err := d.Validate(); err != nil {
			t.Errorf("%+v: %v", d, err)
		}
	}
	bad := map[string]DropIn{
		"id":            {ID: "../x", Mode: "root"},
		"id newline":    {ID: "x\n", Mode: "root"},
		"mode":          {ID: "x", Mode: "user"},
		"root uid":      {ID: "x", Mode: "root", UID: 30001},
		"root grants":   {ID: "x", Mode: "root", Capabilities: []string{"CAP_NET_RAW"}},
		"confined 0":    {ID: "x", Mode: "confined"},
		"system uid":    {ID: "x", Mode: "confined", UID: MinUID - 1},
		"mount":         {ID: "x", Mode: "confined", UID: MinUID, MayMount: true},
		"group occ":     {ID: "x", Mode: "confined", UID: MinUID, Groups: []string{"occulite"}},
		"group shape":   {ID: "x", Mode: "confined", UID: MinUID, Groups: []string{"a b"}},
		"cap admin":     {ID: "x", Mode: "confined", UID: MinUID, Capabilities: []string{"CAP_SYS_ADMIN"}},
		"cap shape":     {ID: "x", Mode: "confined", UID: MinUID, Capabilities: []string{"CAP_X\nUser=root"}},
		"path newline":  {ID: "x", Mode: "confined", UID: MinUID, Paths: []string{"/x\nExecStartPre=+/bin/sh"}},
		"path relative": {ID: "x", Mode: "confined", UID: MinUID, Paths: []string{"x"}},
		"path dots":     {ID: "x", Mode: "confined", UID: MinUID, Paths: []string{"/usr/../etc"}},
		"path spec":     {ID: "x", Mode: "confined", UID: MinUID, Paths: []string{"/%h"}},
	}
	for name, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("%s: %+v accepted", name, d)
		}
	}
}

func TestRender(t *testing.T) {
	if got := (DropIn{ID: "x", Mode: "root"}).Render(); !strings.HasSuffix(got, "# mode=root\n[Service]\nCapabilityBoundingSet=~CAP_SYS_ADMIN\n") {
		t.Errorf("root:\n%s", got)
	}
	if got := (DropIn{ID: "x", Mode: "root", MayMount: true}).Render(); strings.Contains(got, "[Service]") {
		t.Errorf("may mount:\n%s", got)
	}
	got := (DropIn{ID: "x", Mode: "confined", UID: 30002, Groups: []string{"certs"}, Paths: []string{"/media"}}).Render()
	for _, want := range []string{"# mode=confined uid=30002\n[Service]\nUser=addon-x\nGroup=addon-x\nSupplementaryGroups=certs\nCapabilityBoundingSet=\n", "RuntimeDirectory=addon-x\n", " -/var/tmp -/media\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("confined lacks %q:\n%s", want, got)
		}
	}
	if RenderNeeds(nil) != "none\n" || RenderNeeds([]string{"hs485d", "rfd"}) != "rfd hs485d\n" || RenderStart() != "early\n" {
		t.Error("needs or start")
	}
	if ValidateNeeds([]string{"rfd", "rfd"}) == nil || ValidateNeeds([]string{"sshd"}) == nil || ValidateNeeds(nil) != nil {
		t.Error("ValidateNeeds")
	}
}

func TestFileContent(t *testing.T) {
	d := &DropIn{ID: "x", Mode: "root"}
	none := []string{}
	d0, d1, d30, dMax, dNeg, dOver := 0, 1, 30, MaxStartDelay, -5, MaxStartDelay+1
	for _, c := range []struct {
		kind, id string
		f        File
		text     string
		remove   bool
		ok       bool
	}{
		{KindDropIn, "x", File{DropIn: d}, d.Render(), false, true},
		{KindDropIn, "y", File{DropIn: d}, "", false, false},
		{KindDropIn, "x", File{Early: true}, "", false, false},
		{KindNeeds, "x", File{Needs: &none}, "none\n", false, true},
		{KindNeeds, "x", File{DropIn: d}, "", false, false},
		{KindStart, "x", File{Early: true}, "early\n", false, true},
		{KindStart, "x", File{Remove: true}, "", true, true},
		{KindStart, "x", File{Remove: true, Early: true}, "", false, false},
		{KindStart, "x", File{}, "", false, false},
		{".json", "x", File{Early: true}, "", false, false},
		// openccu-lite task 369: the start delay
		{KindDelay, "x", File{Delay: &d30}, "30\n", false, true},
		{KindDelay, "x", File{Delay: &d1}, "1\n", false, true},
		{KindDelay, "x", File{Delay: &dMax}, "600\n", false, true},
		{KindDelay, "x", File{Delay: &d0}, "", false, false},
		{KindDelay, "x", File{Delay: &dNeg}, "", false, false},
		{KindDelay, "x", File{Delay: &dOver}, "", false, false},
		{KindDelay, "x", File{Early: true}, "", false, false},
		{KindDelay, "x", File{Remove: true}, "", true, true},
		{KindDelay, "x", File{Remove: true, Delay: &d30}, "", false, false},
		{KindStart, "x", File{Delay: &d30}, "", false, false},
	} {
		text, remove, err := c.f.Content(c.kind, c.id)
		if (err == nil) != c.ok || text != c.text || remove != c.remove {
			t.Errorf("%s %s %+v: %q %v %v", c.kind, c.id, c.f, text, remove, err)
		}
	}
}
