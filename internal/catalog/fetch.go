package catalog

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/hobbyquaker/occulited/internal/httpwait"
)

// A request that GitHub answers with a 5xx is tried once more on fresh connections (occulited B-37).
//
// On rpi4-1 an install failed four times in a row with "download: HTTP 500", each run over in
// about 30 ms - two round trips on pooled connections - while curl and a fresh Go client on the same
// system fetched the same asset at once, and a restart of occulited ended it. The catalogue's
// client is long-lived (trust.Transport keeps its pool while the trust store does not change), so
// a connection GitHub's edge keeps answering with an error was used again and again. The failing
// answer was not visible either: the progress said "HTTP 500" and nothing else, and nothing went
// to the journal.
//
// So every GET of the catalogue goes through do: a transport error or a 5xx is logged with what
// identifies it (the URL without its query - a release asset's redirect carries a signature there -
// the host that answered after redirects, the status, GitHub's request id), the client's idle
// connections are closed, and the request is sent once more. A second failure is the caller's, and
// its error names the host and the path.

// retryable: worth one more try on a fresh connection.
func retryable(res *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return res.StatusCode >= 500
}

// do sends req through s.HTTP and, after a transport error or a 5xx, once more on fresh
// connections. The request has no body (every catalogue request is a GET). A cancelled context is
// never retried. Each try waits at most HeaderWait for the response header (B-56: a GitHub that
// accepts and never answers held the request for the client's whole 10 minutes); a body then
// takes as long as the client allows.
func (s *Service) do(req *http.Request) (*http.Response, error) {
	res, err := httpwait.Do(s.HTTP, req, s.HeaderWait)
	if !retryable(res, err) || req.Context().Err() != nil {
		return res, err
	}
	s.logFailed(req, res, err, false)
	if res != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
	}
	s.HTTP.CloseIdleConnections()
	again := req.Clone(req.Context())
	res, err = httpwait.Do(s.HTTP, again, s.HeaderWait)
	if retryable(res, err) && req.Context().Err() == nil {
		s.logFailed(again, res, err, true)
	}
	return res, err
}

// logFailed says which request failed and how, without anything secret: no query, no headers but
// the server's and GitHub's request id.
func (s *Service) logFailed(req *http.Request, res *http.Response, err error, final bool) {
	msg := "catalog: a request failed, trying once more on a fresh connection"
	if final {
		msg = "catalog: a request failed again on a fresh connection"
	}
	attrs := []any{"url", redact(req.URL)}
	if err != nil {
		attrs = append(attrs, "err", err)
	} else {
		attrs = append(attrs, "status", res.StatusCode, "answered_by", answeredBy(res),
			"server", res.Header.Get("Server"), "request_id", res.Header.Get("X-GitHub-Request-Id"), "proto", res.Proto)
	}
	slog.Warn(msg, attrs...)
}

// redact is u without its query, its fragment and any user info: what a log may keep.
func redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.RawQuery, c.ForceQuery, c.Fragment, c.RawFragment, c.User = "", false, "", "", nil
	return c.String()
}

// answeredBy is the URL (redacted) the answer came from after redirects.
func answeredBy(res *http.Response) string {
	if res == nil || res.Request == nil {
		return ""
	}
	return redact(res.Request.URL)
}

// statusError is a non-200 answer's error for a download: the status and where it came from.
func statusError(what string, res *http.Response) error {
	return fmt.Errorf("%s: HTTP %d from %s", what, res.StatusCode, answeredBy(res))
}
