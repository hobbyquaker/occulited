package httpapi

// openccu-lite task 213 (task 120's F-2, homematic-manager B-41): a request that reaches the box
// with the session cookie - SameSite=Lax, which still rides along on a top-level navigation from
// another site and on any request from the same site (another port of this host) - is refused
// when the browser says it comes from elsewhere. The one rule, in three places: lighttpd's gate for
// /addons/ (deploy/lighttpd/occulite-gate.lua, which mirrors crossSite), occulited's addon CGIs
// (RequireSession) and occulited's API for the state-changing methods (Middleware).
//
// A request with the credential in a header (a token, the shell's session as a bearer, an addon's
// X-Occulite-Session call from its backend) is not a browser's ambient credential and is not
// checked; neither is ?sid=, which another site cannot know.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// crossSiteVerdict: pass, a top-level navigation from elsewhere (which opens a page, never with
// its query), or a refusal with its reason.
type crossSiteVerdict struct {
	navigate bool
	refuse   string
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// crossSite reads the browser's own statement of where a request comes from:
//   - Sec-Fetch-Site same-origin or none (a typed URL, a bookmark): passes;
//   - cross-site or same-site: a top-level navigation (a safe method, mode navigate, destination
//     document) is a navigation; anything else is refused;
//   - no Sec-Fetch-Site (an older browser, a program): a safe method passes; a state-changing one
//     passes when Origin - else Referer - names this host, or when neither is sent (a program).
func crossSite(r *http.Request) crossSiteVerdict {
	safe := safeMethod(r.Method)
	switch sfs := r.Header.Get("Sec-Fetch-Site"); sfs {
	case "same-origin", "none":
		return crossSiteVerdict{}
	case "cross-site", "same-site":
		if safe && r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" {
			return crossSiteVerdict{navigate: true}
		}
		return crossSiteVerdict{refuse: "Sec-Fetch-Site: " + sfs}
	}
	if safe {
		return crossSiteVerdict{}
	}
	for _, h := range []string{"Origin", "Referer"} {
		v := r.Header.Get(h)
		if v == "" {
			continue
		}
		if u, err := url.Parse(v); err == nil && u.Host != "" && strings.EqualFold(u.Host, requestHost(r)) {
			return crossSiteVerdict{}
		}
		return crossSiteVerdict{refuse: h + ": " + v}
	}
	return crossSiteVerdict{}
}

// ambient says whether the request's session is the browser's ambient credential: the id one of
// the session or gate cookies carries, or - with authentication off - the anonymous session of any
// request that brings no Authorization header. A token, a bearer header, or the legacy alias and
// the session id an addon page's ?sid= carries (RequireSession) are not: another site can neither
// set a header nor know the id.
func (a *AuthAPI) ambient(r *http.Request, sess *auth.Session) bool {
	if sess == nil || sess.IsToken() {
		return false
	}
	if a.cookieAmbient(r, sess) {
		return true
	}
	return a.Off && r.Header.Get("Authorization") == ""
}

// cookieAmbient is ambient's cookie half: a session or gate cookie of the request names the
// session. It is what the header credential of task 259 hangs on - a program on a system without
// login sends no cookie and owes no header.
func (a *AuthAPI) cookieAmbient(r *http.Request, sess *auth.Session) bool {
	if sess == nil || sess.IsToken() {
		return false
	}
	for _, id := range append(append(cookieSIDs(r, SecureCookieName), cookieSIDs(r, CookieName)...), gateCookieSIDs(r)...) {
		if id == sess.ID {
			return true
		}
	}
	return false
}

func refuseCrossSite(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, apiError{Error: "cross-site", Message: "a request from another site with this system's session is refused"})
}
