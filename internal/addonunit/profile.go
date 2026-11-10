package addonunit

import (
	"fmt"
	"strings"

	"github.com/hobbyquaker/occulited/internal/manifest"
)

// The profile snippet of runtime.bin (occulited task 136): an addon's bin directory on the login
// shell's PATH. The image's /etc/profile sources /usr/local/etc/profile.d/*.sh; the system writes
// one snippet per addon that declares runtime.bin and is installed and switched on, and removes it
// when the addon is switched off or uninstalled. The text is rendered here, from the id and the
// checked relative path - never taken from the addon - and written by the privilege helper alone
// (root's login shell runs it).

const (
	// ProfileDir is the userfs directory the image's /etc/profile sources (*.sh).
	ProfileDir = "/usr/local/etc/profile.d"
	// ProfilePrefix and ProfileSuffix make a snippet's name: addon-<id>.sh.
	ProfilePrefix = "addon-"
	ProfileSuffix = ".sh"
	// AddonsDir is where the addons' trees are, /usr/local/addons/<id>.
	AddonsDir = "/usr/local/addons"
)

// ProfileName is the snippet's file name for an addon id.
func ProfileName(id string) string { return ProfilePrefix + id + ProfileSuffix }

// ProfileID is the addon id of a snippet's file name, false for a name that is not one.
func ProfileID(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, ProfilePrefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ProfileSuffix)
	if !ok || !IDRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// ProfileSnippet renders the snippet that appends /usr/local/addons/<id>/<bin> to PATH (occulited
// task 136). Three rules keep an addon's commands from becoming root's by accident:
//   - **appended**, never prepended: a command of the system or of BusyBox always wins, an addon
//     adds names and shadows none;
//   - **interactive shells only** (`case $- in *i*`): a unit or a script that sources /etc/profile
//     gets nothing - the commands are for a person at an ssh prompt;
//   - **only a directory that is root's and that nobody else may write**, checked at every login
//     with `ls -ldn`: the system makes the directory root's (ownwalk.RootBin) at the install and every
//     start, but the directories above it stay the addon user's, who could move it aside and make
//     one of its own until the next start.
//
// Both parts are checked: an id of the policy files' shape and a bin by manifest.ValidBin, whose
// characters need no quoting. The check runs in a function of its own, so the login shell's
// positional parameters are not touched, and the function is removed again.
func ProfileSnippet(id, bin string) (string, error) {
	if !IDRe.MatchString(id) {
		return "", fmt.Errorf("not an addon id: %q", id)
	}
	if err := manifest.ValidBin(bin); err != nil {
		return "", err
	}
	dir := AddonsDir + "/" + id + "/" + bin
	return "# openccu-lite addon " + id + ": runtime.bin of its manifest, the addon's bin directory at the end of\n" +
		"# PATH in interactive login shells, while it is installed and switched on and the directory is root's\n" +
		"# and writable by nobody else. Written by occulited, removed with the addon; not a file to edit.\n" +
		"_occulite_bin_ok() {\n" +
		"\tset -- $(command ls -ldn \"$1\" 2>/dev/null)\n" +
		"\tcase \"$1:$3\" in d????-??-?*:0) return 0 ;; esac\n" +
		"\treturn 1\n" +
		"}\n" +
		"case $- in\n" +
		"*i*)\n" +
		"\tif _occulite_bin_ok \"" + dir + "\"; then\n" +
		"\t\texport PATH=\"${PATH:+${PATH}:}" + dir + "\"\n" +
		"\tfi\n" +
		"\t;;\n" +
		"esac\n" +
		"unset -f _occulite_bin_ok\n", nil
}
