// Package button is the RPI-RF-MOD's button (openccu-lite task 164): the gestures read from its
// GPIO line - single, double and long press - and what the system does on them.
//
// The button is GPIO12 on the Pi's header, active low (the module's 10 kΩ pull-up and the Pi's
// own; pressed = low). The privileged helper reads the line through the GPIO character device
// (gpio_linux.go) and runs the Detector, because the long press's recovery reboot must work when
// the daemon does not (the maintainer, 2026-10-15); the daemon takes the gestures from the helper
// and runs the configured actions (service.go). Pure here: no I/O, the clock passed in, so the
// table tests drive it.
package button

import "time"

// The gestures and the Detector's other events.
const (
	// EventPress and EventRelease are the line's edges as the Detector took them.
	EventPress   = "press"
	EventRelease = "release"
	// EventLongReached: the press has been held for the long threshold - the LED says "release
	// now"; releasing makes it a long press, holding on past the cancel time makes it nothing.
	EventLongReached = "long-reached"
	// The gestures, at release (single after the double-press window has passed).
	GestureSingle = "single"
	GestureDouble = "double"
	GestureLong   = "long"
	// EventCancel: held past the cancel time - someone is trying the power-on recovery or leaning
	// on the button; nothing happens at the release.
	EventCancel = "cancel"
)

// Timing is the Detector's thresholds.
type Timing struct {
	// ShortMax: a press released before this is a short one (single or the half of a double).
	ShortMax time.Duration
	// DoubleWindow: a second short press starting within this after the first one's release
	// makes a double; a single acts only when the window has passed.
	DoubleWindow time.Duration
	// Long: held this long it is a long press (released before Cancel).
	Long time.Duration
	// Cancel: held this long nothing happens.
	Cancel time.Duration
}

// The bounds and defaults of the long threshold (the maintainer, 2026-10-15: 5 s - the CCU3
// manual's figure - configurable 3 to 10 s).
const (
	LongDefault = 5 * time.Second
	LongMin     = 3 * time.Second
	LongMax     = 10 * time.Second
)

// DefaultTiming is the Detector's timing with the long threshold long (LongDefault when 0).
func DefaultTiming(long time.Duration) Timing {
	if long <= 0 {
		long = LongDefault
	}
	return Timing{ShortMax: time.Second, DoubleWindow: 400 * time.Millisecond, Long: long, Cancel: 15 * time.Second}
}

// Event is one thing the Detector says.
type Event struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// HeldMS is how long the press was held, for the release and the gestures.
	HeldMS int64 `json:"held_ms,omitempty"`
}

// Detector turns the line's edges into gestures. Not safe for concurrent use.
type Detector struct {
	T Timing

	down      bool
	downAt    time.Time
	reached   bool // EventLongReached said for this press
	cancelled bool // EventCancel said for this press
	// a short press released, waiting whether a second one makes it a double
	pending   bool
	pendingAt time.Time // its release
	// this press began within the window of a pending single
	second bool
}

// Pressed reports whether the button is held now.
func (d *Detector) Pressed() bool { return d.down }

// Press takes a falling edge (the button pressed) at t.
func (d *Detector) Press(t time.Time) []Event {
	if d.down {
		return nil
	}
	out := d.Tick(t) // a single whose window has run out says so first
	d.down, d.downAt, d.reached, d.cancelled = true, t, false, false
	d.second = d.pending && t.Sub(d.pendingAt) <= d.T.DoubleWindow
	if d.second {
		d.pending = false
	}
	return append(out, Event{Kind: EventPress, At: t})
}

// Release takes a rising edge (the button released) at t.
func (d *Detector) Release(t time.Time) []Event {
	if !d.down {
		return nil
	}
	out := d.Tick(t)
	if !d.down { // Tick may not end a press, but keep the guard
		return out
	}
	held := t.Sub(d.downAt)
	d.down = false
	out = append(out, Event{Kind: EventRelease, At: t, HeldMS: held.Milliseconds()})
	second := d.second
	d.second = false
	switch {
	case d.cancelled || held >= d.T.Cancel:
		// nothing: said at the cancel time
	case held >= d.T.Long:
		out = append(out, Event{Kind: GestureLong, At: t, HeldMS: held.Milliseconds()})
	case held < d.T.ShortMax && second:
		out = append(out, Event{Kind: GestureDouble, At: t, HeldMS: held.Milliseconds()})
	case held < d.T.ShortMax:
		d.pending, d.pendingAt = true, t
	}
	// between short and long: nothing
	return out
}

// Tick lets time pass to t: the long threshold, the cancel time, a single whose double-press
// window has run out.
func (d *Detector) Tick(t time.Time) []Event {
	var out []Event
	if d.pending && !d.down && t.Sub(d.pendingAt) > d.T.DoubleWindow {
		d.pending = false
		out = append(out, Event{Kind: GestureSingle, At: d.pendingAt.Add(d.T.DoubleWindow)})
	}
	if d.down {
		held := t.Sub(d.downAt)
		if !d.reached && held >= d.T.Long && held < d.T.Cancel {
			d.reached = true
			out = append(out, Event{Kind: EventLongReached, At: d.downAt.Add(d.T.Long), HeldMS: d.T.Long.Milliseconds()})
		}
		if !d.cancelled && held >= d.T.Cancel {
			d.cancelled = true
			out = append(out, Event{Kind: EventCancel, At: d.downAt.Add(d.T.Cancel), HeldMS: d.T.Cancel.Milliseconds()})
		}
	}
	return out
}

// Next is when Tick has something to say next, zero when nothing is due.
func (d *Detector) Next() time.Time {
	switch {
	case d.down && !d.reached:
		return d.downAt.Add(d.T.Long)
	case d.down && !d.cancelled:
		return d.downAt.Add(d.T.Cancel)
	case d.pending:
		return d.pendingAt.Add(d.T.DoubleWindow + time.Millisecond)
	}
	return time.Time{}
}
