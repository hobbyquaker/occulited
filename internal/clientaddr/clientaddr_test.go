package clientaddr

import (
	"net/http/httptest"
	"testing"
)

func TestOf(t *testing.T) {
	for _, c := range []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"direct from the LAN", "192.0.2.1:1234", nil, "192.0.2.1"},
		{"direct from the loopback", "127.0.0.1:5", nil, "127.0.0.1"},
		{"IPv6 loopback", "[::1]:5", nil, "::1"},
		{"lighttpd, a plain client", "127.0.0.1:5", []string{"198.51.100.5"}, "198.51.100.5"},
		{"lighttpd, IPv6 client", "127.0.0.1:5", []string{"fe80::1"}, "fe80::1"},
		// B-230: a client-sent header comes first, lighttpd's element last
		{"forged range", "127.0.0.1:5", []string{"10.99.1.1, 198.51.100.5"}, "198.51.100.5"},
		{"forged loopback", "127.0.0.1:5", []string{"127.0.0.1, 198.51.100.5"}, "198.51.100.5"},
		{"forged, no spaces", "127.0.0.1:5", []string{"127.0.0.1,198.51.100.5"}, "198.51.100.5"},
		{"forged as its own line", "127.0.0.1:5", []string{"127.0.0.1", "198.51.100.5"}, "198.51.100.5"},
		{"hmipserver through lighttpd", "127.0.0.1:5", []string{"127.0.0.1"}, "127.0.0.1"},
		// the header from anyone but the loopback is ignored
		{"LAN sends the header", "192.0.2.1:1234", []string{"127.0.0.1"}, "192.0.2.1"},
		{"LAN sends a range", "192.0.2.1:1234", []string{"10.99.1.1"}, "192.0.2.1"},
		{"an empty last element", "127.0.0.1:5", []string{"127.0.0.1, "}, ""},
		{"no port", "192.0.2.7", []string{"127.0.0.1"}, "192.0.2.7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := Of(r); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// openccu-lite task 406: the ingress id, only from the loopback.
func TestIngress(t *testing.T) {
	for _, c := range []struct{ remote, header, want string }{
		{"127.0.0.1:5", "wireguard", "wireguard"},
		{"[::1]:5", " tailscale ", "tailscale"},
		{"127.0.0.1:5", "", ""},
		{"192.0.2.1:1234", "wireguard", ""},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.header != "" {
			r.Header.Set(IngressHeader, c.header)
		}
		if got := Ingress(r); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.remote, c.header, got, c.want)
		}
	}
}
