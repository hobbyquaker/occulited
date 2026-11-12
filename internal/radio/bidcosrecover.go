package radio

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// BidCosRecovery puts a BidCos interface back that hmipserver's prep step left out (openccu-lite
// task 442, see bidcoswait.go): hmipserver reads InterfacesList.xml only at its start, so the
// entry comes back with a restart of the server, and the prep step of that start, finding the
// process answering, writes it back and removes the marker.
//
// It runs inside occulited and does nothing while there is no marker - one file read per tick.
// With one, it asks every tick whether the left-out processes answer now: rfd's unit active
// (its ready step passes only once rfd's status file names it, so a LAN gateway that answers -
// not just a port rfd opens at its start) and its port open; hs485d the same, its unit having no
// ready step of its own. Once they have answered for Stable without a break, and hmipserver is up
// (a start is never cut short), it restarts hmipserver once.
//
// No flapping: a restart that comes back without them again (the marker's At is newer than the
// restart) is counted, and each next one waits for twice the time of answering before (30 s,
// 1, 2, 4, 8 minutes); after MaxRestarts it gives up for this marker, which the Status warning
// says - a restart of hmipserver by hand, or the reboot, starts afresh. The count is kept per
// marker (its Since) and in memory: a restart of occulited starts it afresh too.
type BidCosRecovery struct {
	Root string
	// Every is the tick; 0 = 10 s. Stable the first time of answering; 0 = 30 s. MaxRestarts the
	// cap; 0 = 5.
	Every       time.Duration
	Stable      time.Duration
	MaxRestarts int
	// Answering says whether the process answers; ServerUp whether hmipserver's unit is active;
	// Restart restarts it without waiting for the start. nil Answering/ServerUp ask systemctl
	// through Exec.
	Answering func(ctx context.Context, w BidCosWait) bool
	ServerUp  func(ctx context.Context) bool
	Restart   func(ctx context.Context) error
	Exec      Runner
	Log       *slog.Logger
	Now       func() time.Time

	mu        sync.Mutex
	since     time.Time // the marker the state below belongs to
	upSince   time.Time // since when every left-out process answers; zero: not all do
	restarts  int
	restartAt time.Time // the restart asked for and not yet seen come back
	gaveUp    bool
}

// BidCosRecoveryStatus is the watch's state for the Status warning.
type BidCosRecoveryStatus struct {
	Restarts int  `json:"restarts"`
	GaveUp   bool `json:"gave_up"`
}

// Status answers the state for the marker there is now.
func (r *BidCosRecovery) Status() BidCosRecoveryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return BidCosRecoveryStatus{Restarts: r.restarts, GaveUp: r.gaveUp}
}

// Run ticks until the context ends.
func (r *BidCosRecovery) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = 10 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *BidCosRecovery) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *BidCosRecovery) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Tick is one look at the marker; exported for the tests.
func (r *BidCosRecovery) Tick(ctx context.Context) {
	m := ReadBidCosDegraded(r.Root)
	r.mu.Lock()
	defer r.mu.Unlock()
	if m == nil || len(m.Missing) == 0 {
		if !r.since.IsZero() && r.restarts > 0 {
			r.log().Info("radio: BidCos back in InterfacesList.xml after the restart of hmipserver", "restarts", r.restarts)
		}
		r.since, r.upSince, r.restarts, r.restartAt, r.gaveUp = time.Time{}, time.Time{}, 0, time.Time{}, false
		return
	}
	if !m.Since.Equal(r.since) {
		r.since, r.upSince, r.restarts, r.restartAt, r.gaveUp = m.Since, time.Time{}, 0, time.Time{}, false
	}
	if r.gaveUp {
		return
	}
	now := r.now()
	if !r.restartAt.IsZero() {
		if !m.At.After(r.restartAt) {
			// the restart's prep step has not run yet (or waits); 10 minutes covers its wait and
			// a start, after that the restart counts as come back
			if now.Sub(r.restartAt) < 10*time.Minute {
				return
			}
		}
		r.log().Warn("radio: hmipserver came back without BidCos again: the process answered, then did not at the restart", "missing", waitNames(m.Missing), "restarts", r.restarts)
		r.restartAt, r.upSince = time.Time{}, time.Time{}
	}
	for _, w := range m.Missing {
		if !r.answering(ctx, w) {
			r.upSince = time.Time{}
			return
		}
	}
	if r.upSince.IsZero() {
		r.upSince = now
		r.log().Info("radio: a BidCos process hmipserver started without answers again; hmipserver restarts once it stays up", "missing", waitNames(m.Missing), "after", r.stable()<<r.restarts)
		return
	}
	if now.Sub(r.upSince) < r.stable()<<r.restarts {
		return
	}
	if r.restarts >= r.maxRestarts() {
		r.gaveUp = true
		r.log().Warn("radio: gave up putting BidCos back: it did not stay up after the restarts of hmipserver; a restart of hmipserver by hand tries again", "missing", waitNames(m.Missing), "restarts", r.restarts)
		return
	}
	if !r.serverUp(ctx) {
		return
	}
	if r.Restart == nil {
		return
	}
	if err := r.Restart(ctx); err != nil {
		r.log().Warn("radio: hmipserver could not be restarted to put BidCos back", "err", err)
		return
	}
	r.restarts++
	r.restartAt = now
	r.log().Info("radio: hmipserver restarted to put BidCos back into InterfacesList.xml", "missing", waitNames(m.Missing), "restart", r.restarts)
}

func (r *BidCosRecovery) stable() time.Duration {
	if r.Stable > 0 {
		return r.Stable
	}
	return 30 * time.Second
}

func (r *BidCosRecovery) maxRestarts() int {
	if r.MaxRestarts > 0 {
		return r.MaxRestarts
	}
	return 5
}

func (r *BidCosRecovery) run() Runner {
	if r.Exec != nil {
		return r.Exec
	}
	return ExecRunner
}

func (r *BidCosRecovery) answering(ctx context.Context, w BidCosWait) bool {
	if r.Answering != nil {
		return r.Answering(ctx, w)
	}
	state, _ := unitState(ctx, r.run(), w.Daemon)
	return state == "active/running" && bidcosDial(w.Port)
}

func (r *BidCosRecovery) serverUp(ctx context.Context) bool {
	if r.ServerUp != nil {
		return r.ServerUp(ctx)
	}
	state, _ := unitState(ctx, r.run(), "hmipserver")
	return state == "active/running"
}
