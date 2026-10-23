package devstate

import "strings"

// The chosen set (task 194, the maintainer's pick of 2026-09-24): the datapoints the App page's
// cards draw, derived from its card code (ui/src/lib/app/channels.ts: every state and command key
// of the widget table and what stateText/isOn read; Sheet.svelte: the readings and the controls'
// values; badges.ts: the maintenance keys), plus the service datapoints. A card that needs
// another datapoint adds it here - this list is the one place.
//
// Not kept on purpose: WORKING and PROCESS (a ramp's moment, not a state anyone asks "since when"
// of), write-only commands (STOP, OPEN, the *_COMMAND and *_SELECTION keys: nothing reports them).
var cardKeys = []string{
	// switch, door, contact, lock, the generic STATE (channels.ts)
	"STATE", "DOOR_STATE", "LOCK_STATE",
	// dimmer, blind, colour, signal (LEVEL; the slats LEVEL_2; the colour controls of the sheet)
	"LEVEL", "LEVEL_2", "COLOR", "COLOR_TEMPERATURE", "HUE", "SATURATION",
	// thermostat: the set point in its three spellings, the actual temperature, the modes, the
	// window the thermostat reports itself; the boost's time left (occulited B-103: HmIP BOOST_TIME in seconds, BidCos
	// BOOST_STATE - and the HM-CC-RT-DN-BoM's BOOST_TIME - in minutes), so a page opened during a boost counts down
	"SET_POINT_TEMPERATURE", "SET_TEMPERATURE", "SETPOINT", "ACTUAL_TEMPERATURE",
	"SET_POINT_MODE", "CONTROL_MODE", "BOOST_MODE", "BOOST_TIME", "BOOST_STATE", "WINDOW_STATE", "WINDOW_OPEN",
	// the thermostat tile's valve and humidity on BidCos (channels.ts valvePercent, thermoParts: the HM-CC-RT-DN's
	// VALVE_STATE, the HM-TC-IT's ACTUAL_HUMIDITY) - occulited B-110: the page never reads a BidCos channel by
	// itself, so every value a BidCos tile draws has to come from the store
	"VALVE_STATE", "VALVE_LEVEL", "ACTUAL_HUMIDITY",
	// motion and presence
	"MOTION", "PRESENCE_DETECTION_STATE", "PASSAGE_COUNTER_VALUE", "MOTION_DETECTION_ACTIVE",
	// smoke detector and sirens
	"SMOKE_DETECTOR_ALARM_STATUS", "ACOUSTIC_ALARM_ACTIVE", "OPTICAL_ALARM_ACTIVE",
	// keys: when was it last pressed
	"PRESS_SHORT", "PRESS_LONG",
	// the readings of the sheet (Sheet.svelte READINGS)
	"POWER", "ENERGY_COUNTER", "VOLTAGE", "CURRENT", "FREQUENCY",
	"TEMPERATURE", "HUMIDITY", "ILLUMINATION", "CURRENT_ILLUMINATION",
	"WIND_SPEED", "WIND_DIR", "WIND_DIRECTION", "RAIN_COUNTER", "SUNSHINEDURATION",
	"CARBON_DIOXIDE_CONCENTRATION", "MASS_CONCENTRATION_PM_2_5_24H_AVERAGE",
	"MASS_CONCENTRATION_PM_10_24H_AVERAGE", "SOIL_MOISTURE", "DISTANCE",
	// the rain and water detectors' state the reading tile's icon shows (stateicons.ts readingIcon; B-110)
	"RAINING", "MOISTURE_DETECTED", "WATERLEVEL_DETECTED", "ALARM_STATE",
}

// serviceKeys are the maintenance datapoints: the CCU's service messages (the ones whose
// description carries the service flag, FLAGS & 8, on the devices in the lab) and the card's
// badges (badges.ts). Every ERROR_* datapoint counts as well.
var serviceKeys = []string{
	"UNREACH", "STICKY_UNREACH", "LOWBAT", "LOW_BAT", "LOW_BATTERY", "CONFIG_PENDING", "UPDATE_PENDING",
	"DEVICE_IN_BOOTLOADER", "SABOTAGE", "FAULT_REPORTING", "ERROR", "ERROR_CODE", "DUTY_CYCLE", "DUTYCYCLE",
}

var keySet = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range cardKeys {
		m[k] = true
	}
	for _, k := range serviceKeys {
		m[k] = true
	}
	return m
}()

// Tracked says whether the store keeps a datapoint: the chosen set.
func Tracked(dp string) bool {
	return keySet[dp] || strings.HasPrefix(dp, "ERROR_")
}

// Keys is the chosen set as a list (the API says it, so a client knows what the bulk read can
// answer); the ERROR_* prefix is written as ERROR_*.
func Keys() []string {
	out := append([]string{}, cardKeys...)
	out = append(out, serviceKeys...)
	return append(out, "ERROR_*")
}
