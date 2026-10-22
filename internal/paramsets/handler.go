package paramsets

import (
	"context"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
)

// Handler fills the cache as devices appear (task 108): on the bus's interface up or restarted
// it sweeps the process's device list (one listDevices, then one getParamsetDescription per
// VALUES key the cache lacks), on newDevices it describes the new ones, and a device that was
// updated, replaced or deleted is forgotten, so its next key is read afresh (a firmware update
// changes the key). One sweep per interface at a time; a second request while one runs is
// queued behind it, not dropped. Attach it with rpcsub.Subscriber.Attach.
type Handler struct {
	Cache *Cache
	// Ctx bounds the sweeps (the daemon's); nil = Background.
	Ctx context.Context
	// Timeout bounds one sweep; 0 = 10 minutes.
	Timeout time.Duration

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// Sweeps counts the sweeps run (tests); Done, when set, is told after each sweep.
	Sweeps int
	Done   func(iface string, asked int, err error)
}

func (h *Handler) ctx() (context.Context, context.CancelFunc) {
	ctx := h.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	t := h.Timeout
	if t == 0 {
		t = 10 * time.Minute
	}
	return context.WithTimeout(ctx, t)
}

func (h *Handler) lock(iface string) *sync.Mutex {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.locks == nil {
		h.locks = map[string]*sync.Mutex{}
	}
	m := h.locks[iface]
	if m == nil {
		m = &sync.Mutex{}
		h.locks[iface] = m
	}
	return m
}

// Event is the bus's datapoint change: nothing for the cache.
func (h *Handler) Event(rpcsub.Event) {}

// Interface is the bus's interface state: up and restarted start a sweep.
func (h *Handler) Interface(name, state string, _ time.Time) {
	if state != "up" && state != "restarted" {
		return
	}
	go h.sweep(name)
}

// Devices is the bus's device-list change.
func (h *Handler) Devices(iface, op string, addresses []string) {
	switch op {
	case "new", "readded":
		go h.describe(iface, addresses)
	case "deleted", "updated", "replaced":
		h.Cache.Forget(iface, addresses)
		if op != "deleted" {
			go h.describe(iface, addresses)
		}
	}
}

// sweep reads the interface's device list and fills what is missing.
func (h *Handler) sweep(iface string) {
	m := h.lock(iface)
	m.Lock()
	defer m.Unlock()
	ctx, cancel := h.ctx()
	defer cancel()
	asked, err := h.sweepCtx(ctx, iface)
	h.mu.Lock()
	h.Sweeps++
	done := h.Done
	h.mu.Unlock()
	if err != nil {
		h.Cache.log().Debug("paramsets: the device list could not be swept", "interface", iface, "err", err)
	} else if asked > 0 {
		h.Cache.log().Info("paramsets: descriptions read from the interface process", "interface", iface, "asked", asked, "held", h.Cache.Len())
	}
	if done != nil {
		done(iface, asked, err)
	}
}

func (h *Handler) sweepCtx(ctx context.Context, iface string) (int, error) {
	if h.Cache.Forward == nil {
		return 0, nil
	}
	v, err := h.Cache.Forward(ctx, iface, "listDevices")
	if err != nil {
		return 0, err
	}
	return h.Cache.Sweep(ctx, iface, v)
}

// describe fills the VALUES descriptions of the addresses' channels (a device address brings
// its children, a channel itself).
func (h *Handler) describe(iface string, addresses []string) {
	m := h.lock(iface)
	m.Lock()
	defer m.Unlock()
	ctx, cancel := h.ctx()
	defer cancel()
	for _, a := range addresses {
		d, err := h.Cache.device(ctx, iface, a)
		if err != nil {
			continue
		}
		chans := []string{a}
		if d.Parent == "" {
			chans = d.Children
		}
		for _, ch := range chans {
			cd, err := h.Cache.device(ctx, iface, ch)
			if err != nil || !has(cd.Paramsets, "VALUES") {
				continue
			}
			if _, _, err := h.Cache.Describe(ctx, iface, ch, "VALUES"); err != nil {
				h.Cache.log().Debug("paramsets: a new device's description could not be read", "interface", iface, "channel", ch, "err", err)
			}
		}
	}
	h.mu.Lock()
	done := h.Done
	h.mu.Unlock()
	if done != nil {
		done(iface, 0, nil)
	}
}
