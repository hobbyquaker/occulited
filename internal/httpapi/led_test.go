package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/led"
	"github.com/hobbyquaker/occulited/internal/priv"
	"github.com/hobbyquaker/occulited/internal/system"
)

// ledRig is the routes on a controller over a fake root: a Charly (available) or a Pi 4 with an
// HmIP-RFUSB (no status LED). The controller runs until the test ends; nothing is really written.
func ledRig(t *testing.T, available bool) (*http.ServeMux, *led.Controller) {
	t.Helper()
	dir := t.TempDir()
	put := func(p, v string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dev := "RPI-RF-MOD"
	if !available {
		dev = "HMIP-RFUSB"
	}
	put("var/hm_mode", "HM_HOST='rpi3'\nHM_MODE='NORMAL'\nHM_HMIP_DEV='"+dev+"'\n")
	for _, l := range led.ChannelLEDs {
		put("sys/class/leds/"+l+"/trigger", "[none] default-on timer")
	}
	for _, f := range []string{"var/status/startupFinished", "var/status/hasLink", "var/status/hasIP"} {
		put(f, "")
	}
	c := &led.Controller{Root: system.Root(dir), File: filepath.Join(dir, "led.json"), Write: func([]priv.LEDWrite) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for c.View().Available != available || (!available && c.View().Reason == "") {
		if time.Now().After(deadline) {
			t.Fatalf("the probe did not run: %+v", c.View())
		}
		time.Sleep(10 * time.Millisecond)
	}
	a := &SystemAPI{Root: system.Root(dir), LED: c}
	mux := http.NewServeMux()
	a.Register(mux)
	return mux, c
}

const ledRole = auth.RoleLED

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLEDRoutes(t *testing.T) {
	mux, c := ledRig(t, true)
	if st, out := warnCall(t, mux, "GET", "/api/system/v1/led", "", "monitor", user); st != 200 || out["available"] != true {
		t.Fatalf("GET /led: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "PUT", "/api/system/v1/led", `{}`, "monitor", user); st != 403 {
		t.Errorf("PUT /led as a user: %d", st)
	}
	if st, out := warnCall(t, mux, "PUT", "/api/system/v1/led", `{"enabled":true,"states":[{"id":"alarm"}]}`, "admin", auth.RoleAdmin); st != 422 {
		t.Errorf("PUT an invalid configuration: %d %v", st, out)
	}
	cfg := mustMarshal(t, func() led.Config {
		d := led.Defaults()
		d.Normal = led.Look{Color: led.Green, Pattern: led.Flash}
		return d
	}())
	if st, out := warnCall(t, mux, "PUT", "/api/system/v1/led", cfg, "admin", auth.RoleAdmin); st != 200 || out["config"].(map[string]any)["normal"].(map[string]any)["color"] != "green" {
		t.Errorf("PUT a configuration: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "GET", "/api/system/v1/led/state", "", "token:hass", ledRole); st != 200 || out["available"] != true {
		t.Errorf("GET /led/state as led: %d %v", st, out)
	}

	// overrides: the led role and administrators, not users
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"color":"red"}`, "monitor", user); st != 403 {
		t.Errorf("override as a user: %d", st)
	}
	st, out := warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"color":"magenta","pattern":"slow","duration_s":0}`, "token:hass", ledRole)
	if st != 200 || out["override"].(map[string]any)["id"] != "token:hass" || out["shown"] == nil {
		t.Fatalf("override as led: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"color":"magenta"}`, "token:hass", ledRole); st != 200 {
		t.Errorf("second override: %d", st)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"color":"magenta"}`, "token:hass", ledRole); st != 429 || out["error"] != "rate-limited" {
		t.Errorf("third override in a second: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"color":"pink"}`, "token:flow", ledRole); st != 422 {
		t.Errorf("a colour that does not exist: %d", st)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/override/token:hass", "", "token:flow", ledRole); st != 403 {
		t.Errorf("another caller's override: %d", st)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/override", "", "token:nobody", ledRole); st != 404 {
		t.Errorf("clearing none: %d", st)
	}
	if st, out := warnCall(t, mux, "DELETE", "/api/system/v1/led/override/token:hass", "", "admin", auth.RoleAdmin); st != 200 || len(out["overrides"].([]any)) != 0 {
		t.Errorf("an administrator clears it: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/overrides", "", "token:hass", ledRole); st != 403 {
		t.Errorf("clear all as led: %d", st)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/overrides", "", "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("clear all as an administrator: %d", st)
	}
	// task 134: an override may blink over the normal colour (green since the PUT above)
	st, out = warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"id":"blink","color":"cyan","pattern":"fast","over_normal":true}`, "token:ha2", ledRole)
	if st != 200 || out["override"].(map[string]any)["over_normal"] != true || out["shown"].(map[string]any)["background"] != "green" {
		t.Errorf("an override over normal: %d %v", st, out)
	}
	st, out = warnCall(t, mux, "POST", "/api/system/v1/led/override", `{"id":"blink","color":"cyan","pattern":"solid","over_normal":true}`, "token:ha2", ledRole)
	if o := out["override"].(map[string]any); st != 200 || o["over_normal"] != nil || out["shown"].(map[string]any)["background"] != nil {
		t.Errorf("a solid override drops over_normal: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/overrides", "", "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("clear all: %d", st)
	}
	cfg = mustMarshal(t, func() led.Config {
		d := led.Defaults()
		d.Normal = led.Look{Color: led.Green, Pattern: led.Flash}
		for i := range d.States {
			d.States[i].OverNormal = d.States[i].ID == led.StateStatusWarning || d.States[i].ID == led.StateRadioDown
		}
		return d
	}())
	if st, out := warnCall(t, mux, "PUT", "/api/system/v1/led", cfg, "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("PUT over_normal: %d %v", st, out)
	} else {
		for _, row := range out["config"].(map[string]any)["states"].([]any) {
			r := row.(map[string]any)
			if want := r["id"] == led.StateStatusWarning; (r["over_normal"] == true) != want {
				t.Errorf("stored row %v (radio-down is solid and drops it)", r)
			}
		}
	}

	// locate for led and administrators, the preview for administrators
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/led/locate", "", "token:hass", ledRole); st != 200 || out["locate"] == nil {
		t.Errorf("locate: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/led/locate", `{"duration_s":9999}`, "admin", auth.RoleAdmin); st != 422 {
		t.Errorf("a long locate: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "DELETE", "/api/system/v1/led/locate", "", "token:hass", ledRole); st != 200 || out["locate"] != nil {
		t.Errorf("stop locate: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/preview", `{"color":"green","pattern":"fast"}`, "token:hass", ledRole); st != 403 {
		t.Errorf("preview as led: %d", st)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/led/preview", `{"color":"yellow","pattern":"alternate","color2":"blue"}`, "admin", auth.RoleAdmin); st != 200 || out["preview"] == nil {
		t.Errorf("preview: %d %v", st, out)
	}
	if s := c.State(); s.Shown.Source != led.SourcePreview {
		t.Errorf("the preview is not shown: %+v", s.Shown)
	}
	if st, _ := warnCall(t, mux, "DELETE", "/api/system/v1/led/preview", "", "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("stop preview: %d", st)
	}
}

func TestLEDRoutesWithoutLED(t *testing.T) {
	mux, _ := ledRig(t, false)
	if st, out := warnCall(t, mux, "GET", "/api/system/v1/led", "", "monitor", user); st != 200 || out["available"] != false || out["reason"] != "no-module" {
		t.Errorf("GET /led: %d %v", st, out)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/system/v1/led/override", `{"color":"red"}`},
		{"POST", "/api/system/v1/led/locate", ""},
		{"POST", "/api/system/v1/led/preview", `{"color":"red"}`},
	} {
		if st, out := warnCall(t, mux, c.method, c.path, c.body, "admin", auth.RoleAdmin); st != 501 || out["error"] != "unsupported" {
			t.Errorf("%s %s: %d %v", c.method, c.path, st, out)
		}
	}
	// the power LED's switch is saved on a box without the RGB LED as well
	d := led.Defaults()
	d.PWRErrorLight = true
	if st, _ := warnCall(t, mux, "PUT", "/api/system/v1/led", mustMarshal(t, d), "admin", auth.RoleAdmin); st != 200 {
		t.Errorf("PUT without a LED: %d", st)
	}
	bare := http.NewServeMux()
	(&SystemAPI{Root: system.Root(t.TempDir())}).Register(bare)
	if st, _ := warnCall(t, bare, "GET", "/api/system/v1/led", "", "monitor", user); st != 501 {
		t.Errorf("no controller: %d", st)
	}
}

// openccu-lite task 164: the mute - the led role and administrators, not users; GET /led/state
// carries muted_until; out-of-bounds minutes are refused; DELETE ends it.
func TestLEDMuteRoutes(t *testing.T) {
	mux, _ := ledRig(t, true)
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/mute", `{"minutes":30}`, "monitor", user); st != 403 {
		t.Errorf("mute as a user: %d", st)
	}
	st, out := warnCall(t, mux, "POST", "/api/system/v1/led/mute", `{"minutes":30}`, "token:hass", ledRole)
	if st != 200 || out["muted_until"] == nil {
		t.Fatalf("mute as led: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "GET", "/api/system/v1/led", "", "monitor", user); st != 200 || out["muted_until"] == nil {
		t.Errorf("GET /led while muted: %d %v", st, out)
	}
	if st, _ := warnCall(t, mux, "POST", "/api/system/v1/led/mute", `{"minutes":2}`, "admin", auth.RoleAdmin); st != 422 {
		t.Errorf("two minutes: %d", st)
	}
	if st, out := warnCall(t, mux, "DELETE", "/api/system/v1/led/mute", "", "admin", auth.RoleAdmin); st != 200 || out["muted_until"] != nil {
		t.Errorf("unmute: %d %v", st, out)
	}
	if st, out := warnCall(t, mux, "POST", "/api/system/v1/led/mute", "", "admin", auth.RoleAdmin); st != 200 || out["muted_until"] == nil {
		t.Errorf("mute without a body (60 min): %d %v", st, out)
	}
}
