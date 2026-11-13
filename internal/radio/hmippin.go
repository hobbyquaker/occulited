package radio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openccu-lite task 318 (D-120, maintainer 2026-10-03, "automatic pin"): once HmIP-RF has run on a
// module, the HmIP network belongs to that module - its identity files in crRFD/data are bound to
// it. Upstream's automatic pick could start hmipserver on another module at the next boot or
// hotplug (a second stick that the pick prefers, the module unplugged or silent), and hmipserver
// then attempts the adapter exchange on its own, rewriting the identity files; nobody was asked.
// So the run records the module HmIP-RF runs on, and on Automatic the plan keeps HmIP-RF there: a
// missing module leaves hmipserver without its module - not started (occulited task 147), as with a
// missing chosen module - and a move is the explicit, confirmed connection change. A system that never ran HmIP-RF (no
// record) keeps the automatic pick.

// HmIPPinFile is the record, on the userfs. It names the board too: a backup restored onto other
// hardware brings it along, and there the restore - confirmed as a move of the network (task 301)
// - leaves the pick to the automatic choice.
const HmIPPinFile = "/usr/local/var/lib/occulite/hmip-module.json"

// HmIPPin is the module HmIP-RF last ran on, on which board.
type HmIPPin struct {
	SGTIN    string    `json:"sgtin"`
	Serial   string    `json:"serial,omitempty"`
	BoardMAC string    `json:"board_mac"`
	At       time.Time `json:"at"`
}

// holds: the record is this board's - the plan keeps HmIP-RF on its module.
func (p *HmIPPin) holds(boardMAC string) bool {
	return p != nil && p.SGTIN != "" && p.BoardMAC != "" && strings.EqualFold(p.BoardMAC, boardMAC)
}

// ReadHmIPPin reads the record under root; nil without one or when it is not readable.
func ReadHmIPPin(root string) *HmIPPin {
	b, err := os.ReadFile(hmipPinPath(root))
	if err != nil {
		return nil
	}
	var p HmIPPin
	if json.Unmarshal(b, &p) != nil || p.SGTIN == "" {
		return nil
	}
	return &p
}

func hmipPinPath(root string) string {
	if root == "" || root == "/" {
		return HmIPPinFile
	}
	return filepath.Join(root, HmIPPinFile)
}

// recordHmIPPin keeps the module the plan runs HmIP-RF on; unchanged records are not rewritten.
// False when nothing was written.
func recordHmIPPin(root string, p Plan, boardMAC string, now time.Time) (bool, error) {
	if p.HmIP == nil || p.HmIP.SGTIN == "" || boardMAC == "" {
		return false, nil
	}
	if old := ReadHmIPPin(root); old != nil && strings.EqualFold(old.SGTIN, p.HmIP.SGTIN) && strings.EqualFold(old.BoardMAC, boardMAC) {
		return false, nil
	}
	path := hmipPinPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	b, _ := json.MarshalIndent(HmIPPin{SGTIN: strings.ToUpper(p.HmIP.SGTIN), Serial: p.HmIP.Serial, BoardMAC: boardMAC, At: now.UTC()}, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
