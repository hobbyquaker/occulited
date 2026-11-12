package httpapi

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hobbyquaker/occulited/internal/auth"
)

// The authentication audit lines (B-231): a login at Info, a refused one and the start of a
// lockout at Warn, with the account name as given, the client address and the method - never a
// password or a token. A brute force must not flood the journal: the attempts refused while a
// lockout holds are Debug (the lockout is the rate limit), and beyond refusedPerMinute Warn lines a
// minute the rest of that minute goes to Debug too, announced once.

const (
	refusedPerMinute = 30
	logNameMax       = 64
)

// refusedLimit counts the Warn lines about refused logins in the current minute.
type refusedLimit struct {
	mu     sync.Mutex
	window time.Time
	n      int
}

// allow reports whether one more Warn line fits this minute, and whether this is the first one
// that does not (to say once that the rest goes to Debug).
func (l *refusedLimit) allow(now time.Time) (ok, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.window) >= time.Minute {
		l.window, l.n = now, 0
	}
	l.n++
	return l.n <= refusedPerMinute, l.n == refusedPerMinute+1
}

// logName is an account name as a caller sent it, fit for a log line: control characters and
// other non-printables replaced, at most logNameMax runes - it is attacker-chosen text.
func logName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == logNameMax {
			b.WriteString("…")
			break
		}
		if !unicode.IsPrint(r) {
			r = '?'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// logLogin is the line of a login that opened a session.
func (a *AuthAPI) logLogin(sess *auth.Session, method string) {
	a.logger().Info("auth: login", "user", sess.User, "role", sess.Role, "method", method, "remote", sess.Remote)
}

// logRefused is the line of a refused login: Warn, unless the flood limit is reached or the
// refusal is one inside a lockout, which are Debug.
func (a *AuthAPI) logRefused(r *http.Request, user, method, reason string, extra ...any) {
	attrs := append([]any{"user", logName(user), "method", method, "remote", remote(r), "reason", reason}, extra...)
	if reason == "locked" {
		a.logger().Debug("auth: login refused", attrs...)
		return
	}
	ok, first := a.refused.allow(time.Now())
	switch {
	case ok:
		a.logger().Warn("auth: login refused", attrs...)
	case first:
		a.logger().Warn("auth: more refused logins this minute; the rest of the minute is logged at debug", "limit", refusedPerMinute)
		fallthrough
	default:
		a.logger().Debug("auth: login refused", attrs...)
	}
}

// logLockout is the line of a lockout that a refused login just started.
func (a *AuthAPI) logLockout(r *http.Request, user string, why auth.Refusal) {
	if !why.LockedUser && !why.LockedRemote {
		return
	}
	attrs := []any{"remote", remote(r), "until", why.Until.Format(time.RFC3339)}
	if why.LockedUser {
		attrs = append(attrs, "user", logName(user))
	}
	a.logger().Warn("auth: login locked after repeated failures", attrs...)
}

// presentsToken reports whether the request carries an API token (Bearer or basic auth; a token
// in ?sid= is not read, openccu-lite task 260 - queryCredential sees it): a refused one is a
// refused login, a stale session cookie of a browser is not.
func presentsToken(r *http.Request) bool {
	for _, id := range sessionIDs(r) {
		if auth.IsToken(id) {
			return true
		}
	}
	return false
}
