package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/auth"
	"github.com/hobbyquaker/occulited/internal/config"
	"github.com/hobbyquaker/occulited/internal/oidc"
)

// occulited task 70: the login page's options - the password form hidden behind a link while the
// provider is in use (only once it has signed someone in), and how a passkey is offered.
func TestLoginPageOptions(t *testing.T) {
	username := "admin"
	prov := fakeProvider(t, &username)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "occulited.json")
	cfg := config.Default()
	cfg.Auth.Mode, cfg.Auth.OIDC.Enabled, cfg.Auth.OIDC.Issuer, cfg.Auth.OIDC.ClientID, cfg.Auth.OIDC.Name, cfg.Auth.OIDC.PasswordLogin = "oidc", true, prov.URL, "x", "authentik", true
	if err := config.Save(cfgFile, cfg); err != nil {
		t.Fatal(err)
	}
	store, _ := auth.Open(dir, auth.Options{})
	_ = store.Setup("admin", "test-passw0rd-1")
	mux := http.NewServeMux()
	a := &AuthAPI{Store: store, OIDC: oidc.New(oidc.Config{Issuer: prov.URL, ClientID: "x"}), OIDCName: "authentik", ConfigFile: cfgFile}
	a.Register(mux)
	srv := httptest.NewServer(a.Middleware(mux))
	defer srv.Close()
	admin := map[string]string{"Cookie": CookieName + "=" + cookieOf(t, srv, `{"username":"admin","password":"test-passw0rd-1"}`)}
	put := func(extra string) (int, map[string]any) {
		t.Helper()
		st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", fmt.Sprintf(`{"mode":"oidc","name":"authentik","issuer":%q,"client_id":"x"%s}`, prov.URL, extra), admin)
		return st, out
	}
	// the defaults
	if st, out, _ := do(t, srv, "GET", "/api/auth/v1/config", "", admin); st != 200 || out["hide_password_form"] != false || out["passkey_offer"] != "autofill" || out["provider_login_seen"] != false {
		t.Fatalf("defaults: %d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/oidc", "", nil); out["hide_password_form"] != false {
		t.Errorf("info default: %v", out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/webauthn", "", nil); out["offer"] != "autofill" {
		t.Errorf("webauthn default: %v", out)
	}
	// hiding the form before the provider has signed anyone in: refused, nothing written
	if st, out := put(`,"hide_password_form":true`); st != 409 || out["error"] != "provider_login_required" {
		t.Fatalf("hide before a provider login: %d %v", st, out)
	}
	// after a provider login it can be hidden; the login page is told, the password login stays
	if loc, c := providerLogin(t, srv, "/"); loc != "/" || c == "" {
		t.Fatalf("provider login: %s", loc)
	}
	if st, out := put(`,"hide_password_form":true`); st != 200 || out["hide_password_form"] != true || out["provider_login_seen"] != true {
		t.Fatalf("hide: %d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/oidc", "", nil); out["hide_password_form"] != true || out["password_login"] != true {
		t.Errorf("info hidden: %v", out)
	}
	if st, _, _ := do(t, srv, "POST", "/api/auth/v1/login", `{"username":"admin","password":"test-passw0rd-1"}`, nil); st != 200 {
		t.Errorf("the password login with the form hidden: %d", st)
	}
	// a PUT without the field keeps it; another mode drops it
	if _, out := put(``); out["hide_password_form"] != true {
		t.Errorf("kept: %v", out)
	}
	if st, out, _ := do(t, srv, "PUT", "/api/auth/v1/config", `{"mode":"local","hide_password_form":true}`, admin); st != 200 || out["hide_password_form"] != false {
		t.Errorf("mode local: %d %v", st, out)
	}
	if _, out, _ := do(t, srv, "GET", "/api/auth/v1/oidc", "", nil); out["hide_password_form"] != false {
		t.Errorf("info after mode local: %v", out)
	}
	// the passkey offer: each value, the default kept out of the file, a wrong one refused
	for _, v := range []string{"prompt", "off", "autofill"} {
		if st, out := put(fmt.Sprintf(`,"passkey_offer":%q`, v)); st != 200 || out["passkey_offer"] != v {
			t.Errorf("offer %s: %d %v", v, st, out)
		}
		if _, out, _ := do(t, srv, "GET", "/api/auth/v1/webauthn", "", nil); out["offer"] != v {
			t.Errorf("webauthn offer %s: %v", v, out)
		}
	}
	if c, _ := config.Load(cfgFile); c.Auth.PasskeyOffer != "" {
		t.Errorf("the default is written: %q", c.Auth.PasskeyOffer)
	}
	if st, out := put(`,"passkey_offer":"always"`); st != 422 || out["error"] != "invalid" {
		t.Errorf("wrong offer: %d %v", st, out)
	}
}

func TestLoginOptionsDefaults(t *testing.T) {
	var c config.AuthConfig
	if c.EffectivePasskeyOffer() != "autofill" || c.HidePasswordFormOn() {
		t.Errorf("zero: %+v", c)
	}
	c.PasskeyOffer = "nonsense"
	if c.EffectivePasskeyOffer() != "autofill" {
		t.Error("an unknown offer is the default")
	}
	c.Mode, c.OIDC.HidePasswordForm = "oidc", true
	if c.HidePasswordFormOn() {
		t.Error("hidden with password login off: there is no form")
	}
	c.OIDC.PasswordLogin = true
	if !c.HidePasswordFormOn() {
		t.Error("not hidden")
	}
	c.Mode = "local"
	if c.HidePasswordFormOn() {
		t.Error("hidden without the provider")
	}
	// without a configuration file the options are the defaults
	a := &AuthAPI{}
	if o := a.loginOptions(); o.EffectivePasskeyOffer() != "autofill" {
		t.Errorf("no file: %+v", o)
	}
}
