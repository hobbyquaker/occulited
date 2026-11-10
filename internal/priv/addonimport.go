package priv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/hobbyquaker/occulited/internal/addonimport"
)

// The addons of an (Open)CCU backup (occulited task 142, D-125): the import's "addons" group.
//
// A root addon's rc.d entry, its web tree and its directory are what root runs, and no generic
// operation reaches them (openccu-lite B-293, B-294). So the import has one operation of its own.
// The daemon opens the checked upload and hands the helper the open file as a descriptor with the
// request - the helper never opens a path the daemon names, as storecopy - and names the addon
// ids. The helper extracts exactly four shapes per id (internal/addonimport): the addon's
// directory, its rc.d entry, its web entry, its config directory. Through os.Root, so nothing in
// the archive leads out of those; regular files, directories and symbolic links only; no setuid
// bit; the rc.d script without its executable bits. An id of which anything is here already is
// refused whole - the operation adds addons and never changes one - and so are the NEO Server's
// names and the web trees' directory.
//
// What it lets through: code from an uploaded archive that root will run once the addon is
// enabled (SetAddonEnabled, after the daemon's ReGa and binary checks). That is the addon
// install's power (install_addon runs an archive's update_script as root) and the restore's
// (restoreBackup.sh replaces /usr/local) - both on the program list already. It gives the daemon
// nothing those two do not, and unlike them it cannot overwrite a file.
const opAddonImport = "addon-import"

// maxImportAddons bounds the ids of one request.
const maxImportAddons = 64

// ImportAddons is the client side: the open backup travels as a descriptor with the request. A
// large addon on a slow card takes minutes.
func (c Client) ImportAddons(in *os.File, _ string, ids []string) ([]addonimport.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res, err := c.callWith(ctx, request{Op: opAddonImport, Args: ids}, in)
	var out []addonimport.Result
	if len(res.Stdout) > 0 {
		if jerr := json.Unmarshal(res.Stdout, &out); jerr != nil && err == nil {
			err = fmt.Errorf("privilege helper: %w", jerr)
		}
	}
	return out, err
}

// ImportAddons extracts the addons ids from the backup in (read from its start) below root.
func (Local) ImportAddons(in *os.File, root string, ids []string) ([]addonimport.Result, error) {
	if st, err := in.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("the backup is not a file")
	}
	if _, err := in.Seek(0, 0); err != nil {
		return nil, err
	}
	return addonimport.Extract(in, ids, addonimport.DestUnder(root))
}

// addonImport is the operation at the boundary: ids and nothing else, each of an addon id's shape.
func (s *Server) addonImport(req request, in *os.File) response {
	if in != nil {
		defer in.Close()
	}
	if !reflect.DeepEqual(req, request{Op: req.Op, Args: req.Args}) {
		return refuse("addon-import takes addon ids and nothing else")
	}
	if in == nil {
		return refuse("addon-import takes the backup as a descriptor")
	}
	if len(req.Args) == 0 || len(req.Args) > maxImportAddons {
		return refuse("addon-import takes 1 to 64 addon ids")
	}
	for _, id := range req.Args {
		if !addonimport.ValidID(id) {
			s.log("helper: refused importing the addon %q: not an addon id", id)
			return refuse("addon id " + id)
		}
	}
	s.log("helper: importing the addons %v from a backup", req.Args)
	res, err := s.ops().ImportAddons(in, s.Policy.Root, req.Args)
	out := response{OK: err == nil}
	if err != nil {
		out.Error = err.Error()
	}
	out.Stdout, _ = json.Marshal(res)
	return out
}
