// Package prerelease orders the prerelease part of a version ("dev.31", "beta.0", "rc.1"): the
// one rule the system update check and the addon catalogue share (openccu-lite task 291).
//
// Plain SemVer 2.0 orders text identifiers in ASCII order, which puts "dev" after "beta" and
// "alpha": a system on 1.0.0-dev.31 would never have been offered 1.0.0-beta.0. Here the known
// tags have a rank of their own, dev < alpha < beta < rc (and the release, which has no prerelease
// part, after all of them - that is the caller's rule, not this package's).
package prerelease

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// CompareVersions orders two whole semantic versions: -1, 0 or 1, and false when one of them is not
// a semantic version. A release outranks every prerelease of its numbers (1.1.0-dev.6 < 1.1.0), the
// prereleases in Compare's order, and build metadata (+3) is not compared. The system update check
// and an addon's requires.lite use this one order.
func CompareVersions(a, b string) (int, bool) {
	ma, mb := versionRe.FindStringSubmatch(a), versionRe.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		x, errx := strconv.Atoi(ma[i])
		y, erry := strconv.Atoi(mb[i])
		if errx != nil || erry != nil {
			return 0, false
		}
		if x != y {
			return cmp.Compare(x, y), true
		}
	}
	switch {
	case ma[4] == "" && mb[4] == "":
		return 0, true
	case ma[4] == "":
		return 1, true
	case mb[4] == "":
		return -1, true
	}
	return Compare(ma[4], mb[4]), true
}

// rank is the order of the known tags; an unknown one is 0, below dev.
var rank = map[string]int{"dev": 1, "alpha": 2, "beta": 3, "rc": 4}

// Compare orders two prerelease strings (without the leading "-") identifier by identifier,
// split at the dots, and returns -1, 0 or 1:
//   - two numbers compare numerically (beta.2 < beta.10), and a number comes before text;
//   - two texts compare by their tag rank, dev < alpha < beta < rc; a text that is none of these
//     (case matters: "Beta" is not "beta") ranks below dev, and two such texts compare in ASCII
//     order;
//   - when one list is a prefix of the other, the shorter one comes first (beta < beta.1).
func Compare(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareIdent(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

func compareIdent(x, y string) int {
	xd, yd := digits(x), digits(y)
	switch {
	case xd && yd:
		// numerically, for numbers of any length: without leading zeros, the longer one is larger
		x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
		if c := cmp.Compare(len(x), len(y)); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	case xd:
		return -1
	case yd:
		return 1
	}
	if c := cmp.Compare(rank[x], rank[y]); c != 0 {
		return c
	}
	return strings.Compare(x, y)
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
