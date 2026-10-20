package main

import (
	"slices"
	"sync"

	"github.com/hobbyquaker/occulited/internal/config"
)

// legacySwitches keeps the legacy session's switches (task 125, httpapi.LegacySessions) in
// occulited.json's addons block: read from the file on every ask - another writer (the auth mode,
// the firmware switch) may have rewritten it - and written back around one lock, so two switches
// flipped at once do not lose each other.
type legacySwitches struct {
	mu   sync.Mutex
	path string
}

func (l *legacySwitches) LegacySession() (bool, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := config.Load(l.path)
	if err != nil {
		return true, nil
	}
	return cfg.Addons.LegacySessionOn(), cfg.Addons.LegacySessionOff
}

func (l *legacySwitches) SetLegacySession(on bool, off []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := config.Load(l.path)
	if err != nil {
		return err
	}
	cfg.Addons.LegacySession = nil
	if !on {
		cfg.Addons.LegacySession = &on
	}
	cfg.Addons.LegacySessionOff = off
	if len(off) == 0 {
		cfg.Addons.LegacySessionOff = nil
	}
	return config.Save(l.path, cfg)
}

// earlySwitches keeps the early start's switches (task 119, httpapi.EarlyStarts) in the same
// block of occulited.json, the same way: read on every ask, written around one lock.
type earlySwitches struct {
	mu   sync.Mutex
	path string
}

func (e *earlySwitches) EarlyStart() (bool, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := config.Load(e.path)
	if err != nil {
		return true, nil
	}
	return cfg.Addons.EarlyStartOn(), cfg.Addons.EarlyStartOff
}

func (e *earlySwitches) SetEarlyStart(on bool, off []string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := config.Load(e.path)
	if err != nil {
		return err
	}
	cfg.Addons.EarlyStart = nil
	if !on {
		cfg.Addons.EarlyStart = &on
	}
	cfg.Addons.EarlyStartOff = off
	if len(off) == 0 {
		cfg.Addons.EarlyStartOff = nil
	}
	return config.Save(e.path, cfg)
}

// On is the switch for one addon: on globally and not off for it.
func (e *earlySwitches) On(id string) bool {
	on, off := e.EarlyStart()
	return on && !slices.Contains(off, id)
}

// StartDelays answers every addon's start delay (openccu-lite task 369, httpapi.StartDelays) from
// occulited.json's addons block, read on every ask like the switches above.
func (e *earlySwitches) StartDelays() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := config.Load(e.path)
	if err != nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(cfg.Addons.StartDelay))
	for id, s := range cfg.Addons.StartDelay {
		if s > 0 {
			out[id] = s
		}
	}
	return out
}

// SetStartDelay sets one addon's delay; 0 removes it.
func (e *earlySwitches) SetStartDelay(id string, seconds int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := config.Load(e.path)
	if err != nil {
		return err
	}
	if seconds > 0 {
		if cfg.Addons.StartDelay == nil {
			cfg.Addons.StartDelay = map[string]int{}
		}
		cfg.Addons.StartDelay[id] = seconds
	} else {
		delete(cfg.Addons.StartDelay, id)
		if len(cfg.Addons.StartDelay) == 0 {
			cfg.Addons.StartDelay = nil
		}
	}
	return config.Save(e.path, cfg)
}

// StartDelay is one addon's delay, 0 for none.
func (e *earlySwitches) StartDelay(id string) int { return e.StartDelays()[id] }
