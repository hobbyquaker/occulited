package httpapi

import (
	"errors"
	"net/http"

	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/system"
)

// ---- System -> Remote access: classic CCU RPC (task 143, D-95) ---------------------------------
//
// GET/PUT /remote-access: the two switches (plain, TLS) and the authentication (none, or the pair
// set through PUT /remote-access/classic-password). The firewall is not set here (task 157): each
// open port has classic RPC's owned rule, listed read-only beside the settings. lite-rpc adds its
// own object beside "classic" with task 77.

// remotePort is one classic port as the page lists it.
type remotePort struct {
	system.ClassicRPCPort
	// Open: its switch is on, so lighttpd serves it.
	Open bool `json:"open"`
	// Running: the interface process listens on its loopback port, so the port answers.
	Running bool `json:"running"`
}

type remoteAccessView struct {
	Classic system.ClassicRPC `json:"classic"`
	Ports   []remotePort      `json:"ports"`
	// HS485D: hs485d runs, so 2000 and 42000 are among the ports.
	HS485D bool `json:"hs485d"`
	// Rules are classic RPC's owned firewall rules, in the list's order.
	Rules []firewall.Rule `json:"rules"`
	// Firewall is what the confirmed rules do with each open port and the page's hint for it
	// (openccu-lite task 223: the access points' panel instead of the rule table); nil on a
	// system without the rule file.
	Firewall *classicFirewall `json:"firewall"`
	// Lite is lite-rpc's read-only values (task 77; always on, D-117).
	Lite liteView `json:"lite"`
	// Public is the Control app's public mode (task 193).
	Public publicView `json:"public"`
}

// classicFirewall is the verdict of each port whose switch is on (firewall.Verdict: the first
// enabled rule for it, or the policy, and every source an accepted port is accepted from) and the
// hint:
//   - closed: no port is switched on, so the firewall has nothing to let in;
//   - open: every open port is accepted, from the sources its rules name;
//   - blocked: an open port is not accepted - the clients cannot reach it.
type classicFirewall struct {
	Ports []firewall.PortVerdict `json:"ports"`
	Hint  string                 `json:"hint"`
}

func classicVerdicts(c firewall.Config, ports []remotePort) *classicFirewall {
	f := &classicFirewall{Ports: []firewall.PortVerdict{}, Hint: "closed"}
	for _, p := range ports {
		if !p.Open {
			continue
		}
		v := firewall.Verdict(c, "tcp", p.Port)
		f.Ports = append(f.Ports, v)
		if f.Hint == "closed" {
			f.Hint = "open"
		}
		if v.Target != "ACCEPT" {
			f.Hint = "blocked"
		}
	}
	return f
}

// publicView is the Control app's public mode as the page shows it.
type publicView struct {
	Enabled bool   `json:"enabled"`
	Account string `json:"account"`
	// Available: the daemon has the switch (a test without the auth API has not).
	Available bool `json:"available"`
}

// PublicSwitch is the auth API's public mode, for the Remote access page.
type PublicSwitch interface {
	PublicView() (on bool, account string)
	SetPublic(on bool, account string) error
}

func (a *SystemAPI) publicView() publicView {
	if a.Public == nil {
		return publicView{Account: "guest"}
	}
	on, account := a.Public.PublicView()
	return publicView{Enabled: on, Account: account, Available: true}
}

func (a *SystemAPI) remoteAccessUnavailable(w http.ResponseWriter) bool {
	if a.ClassicRPC == nil {
		writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "remote access is not available on this system"})
		return true
	}
	return false
}

func (a *SystemAPI) remoteAccessView() remoteAccessView {
	c := a.Root.ReadClassicRPC()
	hs := a.Root.HasHS485D()
	listening := map[int]bool{}
	for _, l := range a.Root.ListeningPorts(firewall.Config{}, nil) {
		if l.Loopback {
			listening[l.Port] = true
		}
	}
	v := remoteAccessView{Classic: c, HS485D: hs, Ports: []remotePort{}, Rules: []firewall.Rule{}}
	for _, p := range system.AllClassicRPCPorts(hs) {
		v.Ports = append(v.Ports, remotePort{ClassicRPCPort: p, Open: (p.TLS && c.TLS) || (!p.TLS && c.Plain), Running: listening[p.Backend]})
	}
	var cfg firewall.Config
	haveRules := true
	if a.FirewallRules != nil {
		cfg = a.FirewallRules.View().Config
	} else if rc, ok := a.Root.ReadRules(); ok {
		cfg = rc
	} else {
		haveRules = false
	}
	if haveRules {
		v.Firewall = classicVerdicts(cfg, v.Ports)
	}
	for _, r := range cfg.Rules {
		if r.Owner == firewall.OwnerRPC {
			v.Rules = append(v.Rules, r)
		}
	}
	v.Lite = a.liteRPCView()
	v.Public = a.publicView()
	return v
}

func (a *SystemAPI) remoteAccess(w http.ResponseWriter, r *http.Request) {
	if a.remoteAccessUnavailable(w) {
		return
	}
	writeJSON(w, 200, a.remoteAccessView())
}

type remoteAccessPutBody struct {
	Classic *struct {
		Plain bool   `json:"plain"`
		TLS   bool   `json:"tls"`
		Auth  string `json:"auth"`
	} `json:"classic"`
	// Public is the Control app's public mode (task 193); a body may carry it alone.
	Public *struct {
		Enabled bool   `json:"enabled"`
		Account string `json:"account"`
	} `json:"public"`
}

func (a *SystemAPI) remoteAccessPut(w http.ResponseWriter, r *http.Request) {
	if a.remoteAccessUnavailable(w) {
		return
	}
	var b remoteAccessPutBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Public != nil {
		if a.Public == nil {
			writeJSON(w, http.StatusNotImplemented, apiError{Error: "unsupported", Message: "the public mode is not available on this system"})
			return
		}
		if err := a.Public.SetPublic(b.Public.Enabled, b.Public.Account); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		if b.Classic == nil {
			writeJSON(w, 200, a.remoteAccessView())
			return
		}
	}
	if b.Classic == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "classic or public is missing"})
		return
	}
	want := system.ClassicRPC{Plain: b.Classic.Plain, TLS: b.Classic.TLS, Auth: b.Classic.Auth}
	if _, err := a.ClassicRPC.Set(r.Context(), want, func(l string) { reqLog(r).Info("remote access: " + l) }); err != nil {
		if errors.Is(err, system.ErrClassicRPCInvalid) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	a.warn.forgetFirewall()
	writeJSON(w, 200, a.remoteAccessView())
}

type classicPasswordBody struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Generate bool   `json:"generate"`
}

// the answer to PUT /remote-access/classic-password: the view, and a generated password once
type classicPasswordView struct {
	remoteAccessView
	Password string `json:"password,omitempty"`
}

func (a *SystemAPI) classicPasswordPut(w http.ResponseWriter, r *http.Request) {
	if a.remoteAccessUnavailable(w) {
		return
	}
	var b classicPasswordBody
	if err := readJSON(r, &b); err != nil {
		badBody(w, err)
		return
	}
	if b.Generate && b.Password != "" {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: "either a password or generate, not both"})
		return
	}
	gen, err := a.ClassicRPC.SetPassword(r.Context(), b.User, b.Password, b.Generate, func(l string) { reqLog(r).Info("remote access: " + l) })
	if err != nil {
		if errors.Is(err, system.ErrClassicRPCInvalid) {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "invalid", Message: err.Error()})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, classicPasswordView{remoteAccessView: a.remoteAccessView(), Password: gen})
}
