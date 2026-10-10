// Package httpwait bounds how long occulited waits for a server that accepted the connection but
// does not answer (occulited B-56).
//
// The release feed, the catalogue and eQ-3's firmware index share long-lived clients whose overall
// timeout is meant for the large downloads (30 minutes for a system image). When GitHub's address
// accepted connections from the lab but sent nothing for a few minutes, the feed request waited
// for that whole timeout: `occulited update check` gave up after its own 2 minutes with "context
// deadline exceeded", the Updates page's check simply hung. So a metadata request (a release list,
// a .sha256, a manifest) is bounded as a whole by Bound, and a download waits at most HeaderWait
// for its response header (Do) before its body may take as long as it needs. Either way, the error
// says which host did not answer within how long (NoAnswerError).
package httpwait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// Meta bounds one metadata request as a whole: connect, TLS, header and body.
	Meta = 45 * time.Second
	// HeaderWait bounds the wait for a download's response header.
	HeaderWait = 30 * time.Second
)

// NoAnswerError is a request the bound ended: the host accepted (or was being dialled) and did not
// answer within After.
type NoAnswerError struct {
	Host  string
	After time.Duration
	Err   error
}

func (e *NoAnswerError) Error() string {
	return fmt.Sprintf("%s did not answer within %s", e.Host, e.After.Round(time.Second))
}

func (e *NoAnswerError) Unwrap() error { return e.Err }

// Seconds is After in whole seconds, for an API answer.
func (e *NoAnswerError) Seconds() int { return int(e.After.Round(time.Second) / time.Second) }

// As returns err's NoAnswerError, if it is one.
func As(err error) (*NoAnswerError, bool) {
	var na *NoAnswerError
	ok := errors.As(err, &na)
	return na, ok
}

// Bound returns ctx bounded by d (Meta when d is 0) and an explain function that turns the error
// of a request made under it into a NoAnswerError when the bound ended it - not the caller's own
// context, which stays the caller's error.
func Bound(ctx context.Context, d time.Duration, host string) (context.Context, func(error) error, context.CancelFunc) {
	if d <= 0 {
		d = Meta
	}
	bounded, cancel := context.WithTimeout(ctx, d)
	explain := func(err error) error {
		if err == nil || ctx.Err() != nil {
			return err
		}
		if _, ok := As(err); ok {
			return err
		}
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return &NoAnswerError{Host: host, After: d, Err: err}
		}
		return err
	}
	return bounded, explain, cancel
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Do sends req through c and gives up when no response header arrived within wait (HeaderWait when
// wait is 0); the body may then take as long as c allows. Closing the body releases the request's
// own context.
func Do(c *http.Client, req *http.Request, wait time.Duration) (*http.Response, error) {
	if wait <= 0 {
		wait = HeaderWait
	}
	parent := req.Context()
	ctx, cancel := context.WithCancel(parent)
	var fired atomic.Bool
	t := time.AfterFunc(wait, func() {
		fired.Store(true)
		cancel()
	})
	res, err := c.Do(req.WithContext(ctx))
	stopped := t.Stop()
	if err == nil && !stopped && fired.Load() {
		// the timer won the race with the header: the body is already cancelled
		res.Body.Close()
		err = context.DeadlineExceeded
	}
	if err != nil {
		cancel()
		if fired.Load() && parent.Err() == nil {
			return nil, &NoAnswerError{Host: req.URL.Host, After: wait, Err: err}
		}
		return nil, err
	}
	res.Body = &cancelBody{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
