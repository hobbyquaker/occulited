package servicemsg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hobbyquaker/occulited/internal/rpcsub"
)

// fileKeys is what the file keeps, as keys() shows the list.
func fileKeys(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f stickyFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range f.Messages {
		out = append(out, m.Address+":"+m.Key)
	}
	return out
}

// openccu-lite B-322: rfd forgets STICKY_UNREACH at its own restart and answers false after a
// reboot. The message stays, with its since, until it is confirmed through occulited.
func TestAStickyMessageSurvivesARestartUntilItIsConfirmed(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": true, "LOWBAT": true})
	fi.set("ABC0000002:0", map[string]any{"UNREACH": false})
	file := filepath.Join(t.TempDir(), FileName)
	since := time.Date(2026, 10, 14, 13, 23, 34, 0, time.UTC)
	now := since
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Now: func() time.Time { return now }, File: file}
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	st.Sweep(context.Background())
	if got := keys(st.List()); got != "ABC0000001:0:LOWBAT=start ABC0000001:0:STICKY_UNREACH=start" {
		t.Fatalf("before the restart: %q", got)
	}
	// only the sticky message is kept in the file
	if got := fileKeys(t, file); len(got) != 1 || got[0] != "ABC0000001:STICKY_UNREACH" {
		t.Fatalf("the file keeps %v", got)
	}

	// the reboot: rfd answers false, and the battery was changed meanwhile
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": false, "LOWBAT": false})
	now = since.Add(5 * time.Minute)
	st2 := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, Now: func() time.Time { return now }, File: file}
	if err := st2.Load(); err != nil {
		t.Fatal(err)
	}
	st2.Sweep(context.Background())
	st2.Interface("HmIP-RF", "up", now)
	st2.Sweep(context.Background())
	st2.read(context.Background(), "HmIP-RF", "ABC0000001")
	st2.Event(rpcsub.Event{Interface: "HmIP-RF", Address: "ABC0000001:0", Key: "STICKY_UNREACH", Value: false, Time: now})
	l := st2.List()
	if keys(l) != "ABC0000001:0:STICKY_UNREACH=start" || !l.Messages[0].Since.Equal(since) {
		t.Fatalf("after the restart: %q %+v", keys(l), l.Messages)
	}
	// a write of false to another datapoint confirms nothing
	st2.Acknowledge("HmIP-RF", "ABC0000001", []string{"LOWBAT"})
	if st2.List().Count != 1 {
		t.Fatal("a write to LOWBAT confirmed the sticky message")
	}
	// Confirm: the message ends, in the file too, and a later start does not bring it back
	st2.Acknowledge("HmIP-RF", "ABC0000001", []string{"STICKY_UNREACH"})
	if st2.List().Count != 0 {
		t.Fatalf("after Confirm: %q", keys(st2.List()))
	}
	st2.read(context.Background(), "HmIP-RF", "ABC0000001")
	if got := fileKeys(t, file); len(got) != 0 {
		t.Fatalf("the file still keeps %v", got)
	}
	st3 := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, File: file}
	if err := st3.Load(); err != nil {
		t.Fatal(err)
	}
	st3.Sweep(context.Background())
	if st3.List().Count != 0 {
		t.Fatalf("a confirmed message came back: %q", keys(st3.List()))
	}
}

// An interface process that restarts while occulited runs forgets the value the same way; once
// it says true again (the device went away once more), its own acknowledgement ends the
// message as before (B-46).
func TestAStickyMessageSurvivesTheProcesssRestart(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": true})
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second}
	st.Sweep(context.Background())
	st.Interface("HmIP-RF", "restarted", time.Now())
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": false})
	st.Sweep(context.Background())
	if st.List().Count != 1 {
		t.Fatal("the process's restart ended the sticky message")
	}
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": true})
	st.Sweep(context.Background())
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false, "STICKY_UNREACH": false})
	st.Sweep(context.Background())
	if st.List().Count != 0 {
		t.Fatalf("the process's acknowledgement did not end it: %q", keys(st.List()))
	}
	// without a restart, false is the acknowledgement as before
	fi.set("ABC0000001:0", map[string]any{"STICKY_UNREACH": true})
	st.Sweep(context.Background())
	fi.set("ABC0000001:0", map[string]any{"STICKY_UNREACH": false})
	st.read(context.Background(), "HmIP-RF", "ABC0000001")
	if st.List().Count != 0 {
		t.Fatalf("an acknowledgement at the process: %q", keys(st.List()))
	}
}

// A kept message of a device that was deleted goes: by the deleteDevices event, by a sweep that
// no longer lists the device, or by a read that no longer has the datapoint. A device whose
// channel 0 did not answer keeps it.
func TestAKeptStickyMessageOfADeletedDeviceGoes(t *testing.T) {
	fi := newFakeIface(t)
	file := filepath.Join(t.TempDir(), FileName)
	at := time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC)
	f := stickyFile{Version: fileVersion, Messages: []Message{
		{Interface: "HmIP-RF", Address: "ABC0000001", Channel: "0", Key: "STICKY_UNREACH", Value: true, Since: at, Seen: "event"},
		{Interface: "HmIP-RF", Address: "ABC0000002", Channel: "0", Key: "STICKY_UNREACH", Value: true, Since: at, Seen: "event"},
		{Interface: "HmIP-RF", Address: "GONE000003", Channel: "0", Key: "STICKY_UNREACH", Value: true, Since: at, Seen: "event"},
		{Interface: "BidCos-RF", Address: "JEQ9000002", Channel: "0", Key: "STICKY_UNREACH", Value: true, Since: at, Seen: "event"},
	}}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, File: file}
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	// ABC0000001 answers without the datapoint, ABC0000002's channel 0 does not answer,
	// GONE000003 is not listed, and BidCos-RF is not swept here
	fi.set("ABC0000001:0", map[string]any{"UNREACH": false})
	fi.mu.Lock()
	fi.fail = map[string]bool{"ABC0000002:0": true}
	fi.mu.Unlock()
	st.Sweep(context.Background())
	if got := keys(st.List()); got != "ABC0000002:0:STICKY_UNREACH=event JEQ9000002:0:STICKY_UNREACH=event" {
		t.Fatalf("after the sweep: %q", got)
	}
	st.Devices("BidCos-RF", "deleted", []string{"JEQ9000002", "JEQ9000002:0", "JEQ9000002:1"})
	if got := keys(st.List()); got != "ABC0000002:0:STICKY_UNREACH=event" {
		t.Fatalf("after deleteDevices: %q", got)
	}
	if got := fileKeys(t, file); len(got) != 1 || got[0] != "ABC0000002:STICKY_UNREACH" {
		t.Fatalf("the file keeps %v", got)
	}
}

// Without File nothing is written, and a store with nothing sticky writes no file.
func TestNoFileWithoutStickyMessages(t *testing.T) {
	fi := newFakeIface(t)
	fi.set("ABC0000001:0", map[string]any{"LOWBAT": true})
	file := filepath.Join(t.TempDir(), FileName)
	st := &Store{Interfaces: fi.interfaces, Timeout: 5 * time.Second, File: file}
	st.Sweep(context.Background())
	if st.List().Count != 1 {
		t.Fatal("no message")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("a file without a sticky message: %v", err)
	}
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Store{File: file}).Load(); err == nil {
		t.Fatal("a broken file loaded")
	}
}
