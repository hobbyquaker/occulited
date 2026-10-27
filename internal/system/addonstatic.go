package system

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// B-120 (D-107): an addon's static files under /addons/<id>/ are served by occulited, not by
// lighttpd - as the unprivileged occulite user, and through os.Root, which follows no link out of
// the addon's tree: a `www/x -> /etc/config/shadow` an addon plants answers 404, as does anything a
// root-only file would have been. The tree is the addon's own directory (the parent of its www,
// wherever the www link leads: /usr/local/addons/<id>, or /opt/<id> for an addon that lives there),
// so a link from www into the addon's lib or app is fine. lighttpd's gate stands in front of every
// request, and RequireSession here too.
type AddonStatic struct {
	Root Root
}

// ServeHTTP expects /addons/<name>/<path>; a directory answers its index.html; a dotfile, a
// CGI, a link that leaves the tree and anything that is not a regular file are 404.
func (s AddonStatic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/addons/"), "/", 2)
	if len(parts) != 2 || !cgiNameRe.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	name, rest := parts[0], path.Clean("/"+parts[1])
	for _, seg := range strings.Split(rest, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".cgi") || strings.HasSuffix(seg, ".ccc") {
			http.NotFound(w, r)
			return
		}
	}
	// the www directory as the link leads to it, and its parent as the tree nothing may leave
	www, err := filepath.EvalSymlinks(s.Root.join(filepath.Join(AddonWWW, name)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	treePath := filepath.Dir(www)
	rel := filepath.Join(filepath.Base(www), filepath.FromSlash(rest))
	tree, err := os.OpenRoot(treePath)
	if err != nil {
		// B-115: opening a directory needs read on it, and a confined addon's own directory may be
		// closed to occulite (0751: addon-own before B-115 closed it when the www was not named
		// after the id). The www itself is then the tree: its files are served, a link from www
		// into the addon's lib answers 404 - the tree is never wider than before.
		treePath, rel = www, filepath.Join(".", filepath.FromSlash(rest))
		if tree, err = os.OpenRoot(treePath); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer tree.Close()
	f, st, ok := openRegular(tree, treePath, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if st.IsDir() {
		f.Close()
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		// lighttpd's index-file.names, minus index.cgi, which the CGI runner takes first
		found := false
		for _, idx := range []string{"index.htm", "index.html", "index.xhtml", "default.htm"} {
			if f, st, ok = openRegular(tree, treePath, filepath.Join(rel, idx)); ok && !st.IsDir() {
				found = true
				break
			} else if ok {
				f.Close()
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(st.Name()))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = staticTypes[ext]
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// openRegular opens rel inside the tree; a link that leaves the tree, a special file, or a missing
// one answers false. os.Root refuses an absolute link target outright; a link whose absolute
// target still lies in the tree (an addon linking www/x to its own lib) is resolved and opened by
// its resolved, link-free path through the same root, so the tree is never left either way.
func openRegular(tree *os.Root, treePath, rel string) (*os.File, fs.FileInfo, bool) {
	f, err := tree.Open(rel)
	if err != nil {
		resolved, rerr := filepath.EvalSymlinks(filepath.Join(treePath, rel))
		if rerr != nil || !underDir(resolved, treePath) {
			return nil, nil, false
		}
		inside, rerr := filepath.Rel(treePath, resolved)
		if rerr != nil {
			return nil, nil, false
		}
		if f, err = tree.Open(inside); err != nil {
			return nil, nil, false
		}
	}
	st, err := f.Stat()
	if err != nil || !(st.Mode().IsRegular() || st.IsDir()) {
		f.Close()
		return nil, nil, false
	}
	return f, st, true
}

// staticTypes are the types Go's table may lack on a box without /etc/mime.types.
var staticTypes = map[string]string{
	".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".ico": "image/x-icon", ".webp": "image/webp",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".wasm": "application/wasm", ".map": "application/json",
	".txt": "text/plain; charset=utf-8", ".xml": "text/xml; charset=utf-8", ".pdf": "application/pdf",
}
