package httpapi

import (
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/hobbyquaker/occulited/internal/clientaddr"
)

// IngressRanges answers the address ranges an addon declared for its web ingress (the stored
// manifest's runtime.web_ingress.ranges), nil for an addon without one or an unknown id.
type IngressRanges func(id string) []netip.Prefix

// IngressGuard is the second check of an addon's web ingress (openccu-lite task 406; lighttpd's
// block is the first). A request that came in through an addon's ingress socket carries the
// addon's id in X-Occulite-Ingress and the address the addon claimed in lighttpd's X-Forwarded-For
// element. The address is accepted as the request's remote - the lockout, a token's ranges, the
// journal - only inside the ranges that addon declared; anything else is refused with 403, never
// taken as the loopback or as a LAN address: an ingress addon that claims an address outside its
// ranges is misbehaving or compromised, and a silent fallback would still let the request through
// under some address. An id no installed addon declares an ingress for is refused the same way.
// A request without the header is untouched.
func IngressGuard(ranges IngressRanges, log *slog.Logger) func(http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := clientaddr.Ingress(r)
			if id == "" {
				next.ServeHTTP(w, r)
				return
			}
			remote := clientaddr.Of(r)
			if !IngressAllows(ranges(id), remote) {
				log.Warn("ingress: a request with an address outside the addon's declared ranges was refused", "addon", id, "remote", remote, "path", r.URL.Path)
				writeJSON(w, http.StatusForbidden, apiError{Error: "ingress-range", Message: "the address the ingress claimed is outside the ranges the addon declared"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IngressAllows says whether remote is an address inside one of the ranges.
func IngressAllows(ranges []netip.Prefix, remote string) bool {
	a, err := netip.ParseAddr(remote)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range ranges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
