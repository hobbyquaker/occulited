package main

import (
	"crypto/sha256"
	"encoding/pem"
	"net"
	"sync"
	"time"

	"github.com/hobbyquaker/occulited/internal/firewall"
	"github.com/hobbyquaker/occulited/internal/system"
)

// pairingLocal says whether an address may ask for pairing (task 219: local networks only): the
// loopback, and what the firewall's "local networks" stands for now.
func pairingLocal(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	var nets []*net.IPNet
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				nets = append(nets, n)
			}
		}
	}
	l := firewall.LocalFor(nets)
	// the user's IPv6 networks count as local here as in the firewall (openccu-lite B-339)
	if c, ok := system.Root("/").ReadRules(); ok {
		l = l.WithV6(c.LocalV6)
	}
	for _, c := range append(l.V4, l.V6...) {
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// certFingerprint is the SHA-256 of the certificate lighttpd serves (the first of the live PEM),
// read through the helper and kept a minute: what a pairing code is bound to over HTTPS.
func certFingerprint(root system.Root) func() []byte {
	var mu sync.Mutex
	var fp []byte
	var at time.Time
	return func() []byte {
		mu.Lock()
		defer mu.Unlock()
		if fp != nil && time.Since(at) < time.Minute {
			return fp
		}
		b, _, err := system.CertInstaller{Root: root}.ReadLive()
		if err != nil {
			return fp
		}
		for {
			var blk *pem.Block
			blk, b = pem.Decode(b)
			if blk == nil {
				break
			}
			if blk.Type == "CERTIFICATE" {
				sum := sha256.Sum256(blk.Bytes)
				fp, at = sum[:], time.Now()
				break
			}
		}
		return fp
	}
}
