package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// openccu-lite task 406: a request through an addon's ingress is let through inside the addon's
// ranges and refused outside them, or for an unknown id; a request without the header is untouched.
func TestIngressGuard(t *testing.T) {
	ranges := func(id string) []netip.Prefix {
		if id == "wireguard" {
			return []netip.Prefix{netip.MustParsePrefix("10.77.0.0/24"), netip.MustParsePrefix("fd77::/64")}
		}
		return nil
	}
	h := IngressGuard(ranges, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, c := range []struct {
		name, remote, ingress string
		xff                   []string
		want                  int
	}{
		{"LAN request without the header", "127.0.0.1:5", "", []string{"192.0.2.1"}, 204},
		{"wireguard, in range", "127.0.0.1:5", "wireguard", []string{"10.77.0.2"}, 204},
		{"wireguard, IPv6 in range", "127.0.0.1:5", "wireguard", []string{"fd77::2"}, 204},
		{"wireguard claims the loopback", "127.0.0.1:5", "wireguard", []string{"127.0.0.1"}, 403},
		{"wireguard claims a LAN address", "127.0.0.1:5", "wireguard", []string{"192.168.1.5"}, 403},
		{"wireguard claims the neighbouring network", "127.0.0.1:5", "wireguard", []string{"10.78.0.2"}, 403},
		{"a forged first element does not count", "127.0.0.1:5", "wireguard", []string{"10.77.0.2, 192.168.1.5"}, 403},
		{"PROXY LOCAL: the socket path", "127.0.0.1:5", "wireguard", []string{"/run/lighttpd-addons/wireguard/proxy.sock"}, 403},
		{"no address at all", "127.0.0.1:5", "wireguard", nil, 403},
		{"an addon without an ingress", "127.0.0.1:5", "nut", []string{"10.77.0.2"}, 403},
		{"the header from the LAN is no ingress", "192.0.2.1:1234", "wireguard", []string{"127.0.0.1"}, 204},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/x", nil)
			r.RemoteAddr = c.remote
			if c.ingress != "" {
				r.Header.Set("X-Occulite-Ingress", c.ingress)
			}
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body.String(), c.want)
			}
			if c.want == 403 && w.Body.String() == "" {
				t.Fatal("no body")
			}
		})
	}
}
