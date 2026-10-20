package firmware

import (
	"context"
	"errors"
	"time"

	"github.com/hobbyquaker/occulited/internal/interfaces"
)

// The install of a transferred firmware (occulited task 68): hmipserver transfers a deployed
// bundle in the background and leaves the device in READY_FOR_UPDATE; the install is the user's
// step - the WebUI's Update button, homematic-manager's install, and here the Updates page's
// Install - and is one installFirmware(<address>) on HmIP-RF. The device then goes through
// DO_UPDATE_PENDING (waiting for it, a battery device until it wakes or its system button is
// pressed) and PERFORMING_UPDATE to UP_TO_DATE with the new FIRMWARE, which the live reads follow
// at the fast rate.

var (
	// ErrNoDevice: no HmIP-RF device of that address is known.
	ErrNoDevice = errors.New("no HmIP-RF device of that address")
	// ErrNotReady: the device is not in READY_FOR_UPDATE - nothing is transferred to install.
	ErrNotReady = errors.New("the device has no transferred firmware to install (not READY_FOR_UPDATE)")
	// ErrInstallRefused: hmipserver answered false.
	ErrInstallRefused = errors.New("hmipserver refused the install")
)

// installTimeout is how long installFirmware may take to answer (it answers at once on the lab;
// the flash itself runs after the answer).
const installTimeout = 30 * time.Second

// InstallResult is what an install request came to.
type InstallResult struct {
	// Address and Type of the device; From the firmware it runs, To the one it installs
	Address string `json:"address"`
	Type    string `json:"type"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Pending: the device sleeps - it installs at its next wake-up or when its system button is
	// pressed (hmipserver's "transmission pending")
	Pending bool `json:"pending"`
}

// Install asks hmipserver to install the transferred firmware on one device, only while it is in
// READY_FOR_UPDATE, and has the live states read at the fast rate so the page follows the
// install.
func (s *Service) Install(ctx context.Context, address string) (InstallResult, error) {
	s.mu.Lock()
	var dev *DeviceStatus
	for i := range s.state.Devices {
		if d := &s.state.Devices[i]; d.Interface == "HmIP-RF" && d.Address == address {
			dev = d
			break
		}
	}
	var res InstallResult
	if dev != nil {
		res = InstallResult{Address: dev.Address, Type: dev.Type, From: dev.Firmware, To: dev.AvailableFirmware}
	}
	ready := dev != nil && dev.UpdateState == "READY_FOR_UPDATE"
	s.mu.Unlock()
	switch {
	case dev == nil:
		return res, ErrNoDevice
	case !ready:
		return res, ErrNotReady
	}
	var ifs []interfaces.Interface
	if s.Interfaces != nil {
		ifs = s.Interfaces()
	}
	started, pending, err := interfaces.InstallFirmware(ctx, ifs, "HmIP-RF", address, installTimeout)
	if err != nil {
		return res, err
	}
	if !started {
		return res, ErrInstallRefused
	}
	res.Pending = pending
	s.InstallSeen("HmIP-RF")
	return res, nil
}
