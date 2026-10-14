package main

import (
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/firmware"
	"github.com/hobbyquaker/occulited/internal/rpcsub"
)

// firmwareBus hands the bus to the device firmware service (occulited task 44): a device-list
// change reads the interface's states again, an UPDATE_PENDING or DEVICE_IN_BOOTLOADER event on
// a maintenance channel is an install under way.
type firmwareBus struct{ fw *firmware.Service }

func (b firmwareBus) Event(e rpcsub.Event) {
	if strings.HasSuffix(e.Address, ":0") {
		b.fw.Event(e.Interface, e.Address, e.Key)
	}
}

func (b firmwareBus) Interface(name, state string, at time.Time) {
	if state == "up" || state == "restarted" {
		b.fw.Refresh(name)
	}
}

func (b firmwareBus) Devices(iface, op string, addresses []string) { b.fw.Devices(iface, op) }
