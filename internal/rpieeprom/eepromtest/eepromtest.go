// Package eepromtest builds the files internal/rpieeprom reads, for the tests of its users
// (openccu-lite task 366): a board, a running bootloader, the image's update and a /boot.
package eepromtest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hobbyquaker/occulited/internal/rpieeprom"
)

// The revision codes of the boards the tests use.
const (
	Pi4Rev11 = "c03111" // Pi 4 Model B 1.1: a VL805 EEPROM of its own
	Pi4Rev15 = "c03115" // Pi 4 Model B 1.5: the VL805's firmware in the bootloader's EEPROM
	Pi5      = "d04170" // Pi 5 8 GB
	Pi3      = "a02082" // Pi 3 Model B: no EEPROM
)

// Image is an EEPROM image of 4 KiB with the build time in it, as rpi-eeprom's carry it.
func Image(ts int64) []byte {
	b := make([]byte, 4096)
	copy(b[1000:], fmt.Sprintf("BOOTLOADER\x00BUILD_TIMESTAMP=%d\x00", ts))
	return b
}

// Sig is rpi-eeprom-digest's file for data.
func Sig(data []byte) []byte {
	h := sha256.Sum256(data)
	return []byte(hex.EncodeToString(h[:]) + "\nts: 1792151357\n")
}

// Write writes a file, making its directory.
func Write(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Tree writes the board's revision, the running bootloader's build time (0 = none) and an empty
// /boot under root, and the image's update of latest (0 = none) with recovery.bin and, for a Pi 4,
// the VL805 files.
func Tree(t testing.TB, root, revision string, current, latest int64) {
	t.Helper()
	var rev [4]byte
	var code uint64
	fmt.Sscanf(revision, "%x", &code)
	binary.BigEndian.PutUint32(rev[:], uint32(code))
	Write(t, filepath.Join(root, rpieeprom.RevisionFile), rev[:])
	if current != 0 {
		var ts [4]byte
		binary.BigEndian.PutUint32(ts[:], uint32(current))
		Write(t, filepath.Join(root, rpieeprom.BuildTimestampFile), ts[:])
	}
	if err := os.MkdirAll(filepath.Join(root, rpieeprom.BootDir), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := rpieeprom.ParseRevision(revision)
	if latest == 0 || b.Chip == "" {
		return
	}
	dir := rpieeprom.ImageFiles(root, b.Chip)
	img := Image(latest)
	Write(t, filepath.Join(dir, rpieeprom.Update), img)
	Write(t, filepath.Join(dir, rpieeprom.UpdateSig), Sig(img))
	Write(t, filepath.Join(dir, rpieeprom.Recovery), []byte("recovery"))
	if b.Chip == rpieeprom.Chip2711 {
		vl := []byte("vl805 firmware")
		Write(t, filepath.Join(dir, rpieeprom.VL805), vl)
		Write(t, filepath.Join(dir, rpieeprom.VL805Sig), Sig(vl))
		Write(t, filepath.Join(dir, rpieeprom.VL805Ver), []byte("000138c0\n"))
	}
}
