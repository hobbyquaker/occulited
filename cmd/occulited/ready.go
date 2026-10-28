package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/hobbyquaker/occulited/internal/sdnotify"
)

// readyPath is what the daemon asks itself before it tells systemd it is ready: the open
// version route, the one an addon's first probe reads (ccu-addon-kit's litesource.Probe).
const readyPath = "/api/meta/v1/version"

// readyPoll is the pause between two self-probes, readyProbeTimeout one probe's limit; variables
// for the test.
var (
	readyPoll         = 100 * time.Millisecond
	readyProbeTimeout = 2 * time.Second
)

// notifyWhenAnswering tells systemd READY=1 once the daemon's own HTTP server answers base +
// readyPath with 200 (occulited task 122). occulited.service is Type=notify: until then the unit
// is "activating", so every unit ordered After=occulited.service - the addons, addons.target -
// waits for an API that answers, instead of for a process that exists (openccu-lite-addons B-7:
// battery started while lighttpd still answered 503 "starting"). It asks through the real server
// and its middleware rather than trusting the listener: a bound socket accepts before Serve runs.
//
// Returns once READY was sent (true), or when ctx ends first (false). Without a notification
// socket it sends nothing and does not probe.
func notifyWhenAnswering(ctx context.Context, n *sdnotify.Notifier, base string, log *slog.Logger) bool {
	if !n.Enabled() {
		return false
	}
	start := time.Now()
	// no proxy from the environment and no connection kept: a few loopback requests at the start
	client := &http.Client{Timeout: readyProbeTimeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	for tries := 1; ; tries++ {
		if answers(ctx, client, base+readyPath) {
			if err := n.Notify("READY=1", "STATUS=serving"); err != nil {
				log.Warn("systemd: READY=1 could not be sent", "err", err)
				return false
			}
			log.Info("systemd: ready - the API answers", "ms", time.Since(start).Milliseconds(), "probes", tries)
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(readyPoll):
		}
	}
}

func answers(ctx context.Context, client *http.Client, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}
