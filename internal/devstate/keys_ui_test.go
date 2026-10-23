package devstate

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// occulited B-110: the Control page never reads a BidCos channel's VALUES by itself, so every
// datapoint a tile draws has to be one the store keeps - or a BidCos tile stays blank until the
// device happens to send that very datapoint. The UI's own tables are the source: the widget
// table's state keys and the reading fallback (ui/src/lib/app/channels.ts), the sheet's readings
// (Sheet.svelte), and the values the tile code reads beside its state key.
func TestEveryTileDatapointIsTracked(t *testing.T) {
	channels := readUI(t, "../../ui/src/lib/app/channels.ts")
	sheet := readUI(t, "../../ui/src/lib/app/Sheet.svelte")

	var keys []string
	// the widget table: {kind: /…/, widget: '…', state: 'A|B|C', …}
	for _, m := range regexp.MustCompile(`state: '([A-Z_0-9|]+)'`).FindAllStringSubmatch(channels, -1) {
		keys = append(keys, strings.Split(m[1], "|")...)
	}
	// the reading fallback of classify: first('ACTUAL_TEMPERATURE|…', desc)
	fallback := regexp.MustCompile(`const reading = first\('([A-Z_0-9|]+)', desc\)`).FindStringSubmatch(channels)
	if fallback == nil {
		t.Fatal("classify's reading fallback not found in channels.ts")
	}
	keys = append(keys, strings.Split(fallback[1], "|")...)
	// the sheet's readings: {key: 'POWER', label: …}
	readings := regexp.MustCompile(`\{key: '([A-Z_0-9]+)', label:`).FindAllStringSubmatch(sheet, -1)
	if len(readings) < 10 {
		t.Fatalf("Sheet.svelte READINGS not found (%d entries)", len(readings))
	}
	for _, m := range readings {
		keys = append(keys, m[1])
	}
	// what the tile code reads beside the state key (thermoParts, valvePercent, the mode badge, the
	// boost, the water and rain icons)
	keys = append(keys,
		"ACTUAL_TEMPERATURE", "HUMIDITY", "ACTUAL_HUMIDITY", "VALVE_STATE", "VALVE_LEVEL", "WINDOW_STATE", "WINDOW_OPEN",
		"SET_POINT_MODE", "CONTROL_MODE", "BOOST_MODE", "BOOST_TIME", "BOOST_STATE",
		"RAINING", "MOISTURE_DETECTED", "WATERLEVEL_DETECTED", "ALARM_STATE",
	)
	if len(keys) < 30 {
		t.Fatalf("only %d tile datapoints found: the UI's tables moved", len(keys))
	}
	for _, k := range keys {
		if k == "" {
			continue
		}
		if !Tracked(k) {
			t.Errorf("the tile datapoint %s is not in the store's set (keys.go): a BidCos tile drawing it stays blank", k)
		}
	}
}

func readUI(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the UI source is part of this test: %v", err)
	}
	return string(b)
}
