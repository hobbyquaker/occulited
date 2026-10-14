// Package devimg maps a device type to its picture (occulited task 39): the CCU's WebUI catalogue
// /www/config/devdescr/DEVDB.tcl - which openccu-lite ships with the pictures under
// /www/config/img/devices/{50,250}, served by lighttpd at the CCU's URLs - names in its DEV_PATHS
// array, per type, the picture of each size. The Status page's service messages show the 50 px one.
//
// The file is Tcl: `array set DEV_PATHS {<type> {{50 <path>} {250 <path>}} ...}`, a type with a
// space in braces. It is read once and again when its modification time changes.
package devimg

import (
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultPath is the catalogue on the image.
const DefaultPath = "/www/config/devdescr/DEVDB.tcl"

// Catalog answers a type's picture.
type Catalog struct {
	Path string

	mu    sync.Mutex
	mtime time.Time
	size  int64
	paths map[string]string // type -> the 50 px picture's URL path
	lower map[string]string // lower-case type -> the same
}

// Image is the URL path of a type's small picture ("/config/img/devices/50/…png"), "" when the
// catalogue has none. An exact match first, then one that ignores case (HMIP-WRC2 / HmIP-WRC2).
func (c *Catalog) Image(typ string) string {
	if typ == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	if p, ok := c.paths[typ]; ok {
		return p
	}
	return c.lower[strings.ToLower(typ)]
}

func (c *Catalog) loadLocked() {
	p := c.Path
	if p == "" {
		p = DefaultPath
	}
	fi, err := os.Stat(p)
	if err != nil {
		c.paths, c.lower, c.mtime, c.size = nil, nil, time.Time{}, 0
		return
	}
	if c.paths != nil && fi.ModTime().Equal(c.mtime) && fi.Size() == c.size {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	c.paths = Parse(string(b))
	c.lower = make(map[string]string, len(c.paths))
	for k, v := range c.paths {
		c.lower[strings.ToLower(k)] = v
	}
	c.mtime, c.size = fi.ModTime(), fi.Size()
}

// Parse reads DEV_PATHS out of the catalogue's text: type -> the 50 px picture's path. Only paths
// under /config/img/devices/ are taken.
func Parse(text string) map[string]string {
	out := map[string]string{}
	i := strings.Index(text, "array set DEV_PATHS")
	if i < 0 {
		return out
	}
	rest := text[i+len("array set DEV_PATHS"):]
	j := strings.IndexByte(rest, '{')
	if j < 0 {
		return out
	}
	body, ok := braced(rest[j:])
	if !ok {
		return out
	}
	items := list(body)
	for k := 0; k+1 < len(items); k += 2 {
		typ := strings.Join(strings.Fields(items[k]), " ")
		for _, pair := range list(items[k+1]) {
			f := list(pair)
			if len(f) == 2 && f[0] == "50" && strings.HasPrefix(f[1], "/config/img/devices/") && !strings.Contains(f[1], "..") {
				out[typ] = f[1]
			}
		}
	}
	return out
}

// braced answers the text inside the brace s starts with, up to its matching brace.
func braced(s string) (string, bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[1:i], true
			}
		}
	}
	return "", false
}

// list splits a Tcl list into its elements: words separated by white space, a braced word as one
// element without its braces.
func list(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '{' {
			inner, ok := braced(s[i:])
			if !ok {
				break
			}
			out = append(out, inner)
			i += len(inner) + 2
			continue
		}
		k := i
		for k < len(s) && s[k] != ' ' && s[k] != '\t' && s[k] != '\n' && s[k] != '\r' {
			k++
		}
		out = append(out, s[i:k])
		i = k
	}
	return out
}
