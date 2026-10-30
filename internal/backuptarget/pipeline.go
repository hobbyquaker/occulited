package backuptarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hobbyquaker/occulited/internal/backupcrypt"
	"github.com/hobbyquaker/occulited/internal/netmount"
	"github.com/hobbyquaker/occulited/internal/shares"
)

// The nightly run and "Back up now" (task 86): the backup is created once and delivered to every
// selected target. Two processes, two units (the fork's):
//
//   - occu-backup-create@<instance>.service runs `occulited -backup create <instance>` as root:
//     createBackup.sh into the staging directory (the read-backs stay on the local disk), the
//     encryption (task 91), and the copies into the directory and mount targets, verified.
//   - occu-backup-deliver@<instance>.service, wanted by it and ordered after it, runs
//     `occulited -backup deliver <instance>` as occulite: the SFTP uploads (a hostile or broken
//     server's answers are parsed without root), the record of what was handed out, the cleanup.
//
// <instance> is nightly (the timer; nothing when the nightly switch is off), all (Back up now for
// every enabled target) or a target's id (that one, enabled or not).

// StagingDir is where a run's files wait between the two units: in /usr/local/tmp, which no
// backup contains and the next boot empties.
const StagingDir = "/usr/local/tmp/occulite-backup"

// RunFile is the staging directory's note from create to deliver.
const RunFile = "run.json"

// The units the daemon starts for Back up now.
const (
	CreateUnit  = "occu-backup-create@%s.service"
	DeliverUnit = "occu-backup-deliver@%s.service"
)

var instanceRe = regexp.MustCompile(`^(nightly|all|[a-z][a-z0-9]{0,15})$`)

// ValidInstance checks a unit instance before anything else looks at it.
func ValidInstance(s string) bool { return instanceRe.MatchString(s) }

// StagedFile is one file a run made.
type StagedFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Encrypted bool   `json:"encrypted"`
}

// Run is run.json.
type Run struct {
	Instance string      `json:"instance"`
	At       time.Time   `json:"at"`
	Failed   bool        `json:"failed,omitempty"`
	Error    string      `json:"error,omitempty"`
	Plain    *StagedFile `json:"plain,omitempty"`
	Age      *StagedFile `json:"age,omitempty"`
	Targets  []string    `json:"targets"`
}

// Pipeline is one run's context.
type Pipeline struct {
	Store   *Store
	Crypt   *backupcrypt.Store
	Staging string
	// Hostname and Version name the file: <hostname>-<version>-<date>.sbk, as createBackup.sh does.
	Hostname string
	Version  string
	// Create runs createBackup.sh with the file's path.
	Create func(ctx context.Context, path string) error
	// UID and GID are occulite's: the staged files, run.json and last.json become theirs.
	UID, GID int
	Now      func() time.Time
	Log      *slog.Logger
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Pipeline) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Select answers the targets an instance means.
func (p *Pipeline) Select(instance string) ([]Target, error) {
	if !ValidInstance(instance) {
		return nil, fmt.Errorf("instance %q: nightly, all or a target id", instance)
	}
	list, err := p.Store.List()
	if err != nil {
		return nil, err
	}
	var out []Target
	for _, t := range list {
		switch instance {
		case "nightly", "all":
			if t.Enabled {
				out = append(out, t)
			}
		default:
			if t.ID == instance {
				out = append(out, t)
			}
		}
	}
	if instance != "nightly" && instance != "all" && len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

func (p *Pipeline) own(path string) {
	if os.Geteuid() == 0 {
		_ = os.Chown(path, p.UID, p.GID)
	}
}

// writeResult writes a target's last.json, occulite's.
func (p *Pipeline) writeResult(t Target, r Result) {
	dir := p.Store.SecretDir(t.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		p.log().Warn("backup: result not written", "target", t.ID, "err", err)
		return
	}
	p.own(dir)
	if err := WriteResult(dir, r); err != nil {
		p.log().Warn("backup: result not written", "target", t.ID, "err", err)
		return
	}
	p.own(filepath.Join(dir, LastFile))
}

func (p *Pipeline) clearStaging() error {
	if err := os.MkdirAll(p.Staging, 0o700); err != nil {
		return err
	}
	p.own(p.Staging)
	entries, err := os.ReadDir(p.Staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(p.Staging, e.Name()))
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// encryptTo writes src encrypted to the recovery key and - when the system has made one - its
// own identity. Read-only on the key state: the root process never creates a key file.
func encryptTo(crypt *backupcrypt.Store, src, dst string) (string, int64, error) {
	st, err := crypt.Load()
	if err != nil {
		return "", 0, err
	}
	if st.Recovery == nil {
		return "", 0, backupcrypt.ErrNoRecoveryKey
	}
	recipients := []string{st.Recovery.Recipient}
	meta := backupcrypt.MetaRecipient{RecoveryFingerprint: st.Recovery.Fingerprint}
	if id, _, ok := crypt.BoxIdentity(); ok {
		box := id.Recipient().String()
		recipients = append(recipients, box)
		meta.BoxFingerprint = backupcrypt.Fingerprint(box)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	cw := &counter{w: io.MultiWriter(out, h)}
	enc, err := backupcrypt.Encrypt(cw, meta, recipients...)
	if err == nil {
		_, err = io.Copy(enc, in)
		if cerr := enc.Close(); err == nil {
			err = cerr
		}
	}
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), cw.n, nil
}

type counter struct {
	w io.Writer
	n int64
}

func (c *counter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// wantsAge says whether a target gets the encrypted file.
func wantsAge(t Target, enabled bool) bool { return t.Encrypt && enabled }

// CreateRun is the root half. It returns an error when the backup could not be made or a
// directory or mount target failed; the deliver unit runs either way.
func (p *Pipeline) CreateRun(ctx context.Context, instance string) error {
	log := p.log()
	if instance == "nightly" && !p.Store.Nightly() {
		log.Info("backup: the nightly backup is switched off")
		return nil
	}
	targets, err := p.Select(instance)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		log.Info("backup: no target is enabled", "instance", instance)
		return nil
	}
	at := p.now()
	// task 86's follow-up (task 161): a USB directory target without its stick is skipped before
	// anything is made - its result says no-medium, the Status warning names it, and neither the
	// other targets nor the unit fail for it (no 99 s backup for nothing when it is the only one)
	var ready []Target
	for _, t := range targets {
		if missing, err := noMedium(t); missing {
			p.writeResult(t, Result{At: at, Instance: instance, State: StateNoMedium, Step: "medium", Error: err.Error()})
			log.Warn("backup: skipped, the USB stick is not there", "target", t.ID, "dir", t.Dir())
			continue
		}
		ready = append(ready, t)
	}
	if len(ready) == 0 {
		log.Info("backup: nothing to deliver to", "instance", instance)
		return nil
	}
	// B-212: a directory or share target that cannot take a file is found before the backup is
	// made - a read-only share (a root-squashed export) or one that does not mount answers in a
	// second, not after a 1.7 GB backup. Such a target fails as its delivery would have.
	var failed []string
	checked := ready
	ready = nil
	for _, t := range checked {
		if t.IsLocal() {
			if r, ok := p.probe(t, at, instance); !ok {
				p.writeResult(t, r)
				failed = append(failed, t.ID)
				log.Warn("backup: skipped, the target cannot take a file", "target", t.ID, "kind", t.Kind, "state", r.State, "step", r.Step, "err", r.Error)
				continue
			}
			// B-247: on the system's own user partition, room for a system update first
			removed, r, ok := precheckRoom(t, p.Staging, at, instance)
			if len(removed) > 0 {
				log.Info("backup: old backups removed to keep room for a system update", "target", t.ID, "dir", t.Dir(), "removed", removed)
			}
			if !ok {
				p.writeResult(t, r)
				failed = append(failed, t.ID)
				log.Warn("backup: skipped, it would leave too little room for a system update", "target", t.ID, "dir", t.Dir(), "err", r.Error)
				continue
			}
		}
		ready = append(ready, t)
	}
	if len(ready) == 0 {
		return fmt.Errorf("delivery failed: %s", strings.Join(failed, ", "))
	}
	targets = ready
	run := Run{Instance: instance, At: at}
	if err := p.clearStaging(); err != nil {
		err = fmt.Errorf("staging: %w", err)
		for _, t := range targets {
			p.writeResult(t, Result{At: run.At, Instance: instance, State: StateError, Step: "staging", Error: err.Error()})
		}
		return err
	}
	for _, t := range targets {
		run.Targets = append(run.Targets, t.ID)
	}
	writeRun := func() {
		b, _ := json.MarshalIndent(run, "", "  ")
		path := filepath.Join(p.Staging, RunFile)
		if err := writeAtomic(path, b, 0o600); err != nil {
			log.Warn("backup: run.json not written", "err", err)
		}
		p.own(path)
	}
	failAll := func(step string, err error) error {
		run.Failed, run.Error = true, err.Error()
		for _, t := range targets {
			p.writeResult(t, Result{At: run.At, Instance: instance, State: StateError, Step: step, Error: err.Error()})
		}
		writeRun()
		return err
	}
	host := p.Hostname
	if host == "" {
		host = "openccu-lite"
	}
	ver := p.Version
	if ver == "" {
		ver = "unknown"
	}
	name := fmt.Sprintf("%s-%s-%s.sbk", host, ver, run.At.Format("2006-01-02-1504"))
	plainPath := filepath.Join(p.Staging, name)
	start := time.Now()
	if err := p.Create(ctx, plainPath); err != nil {
		return failAll("create", fmt.Errorf("createBackup.sh: %w", err))
	}
	sum, size, err := hashFile(plainPath)
	if err != nil || size == 0 {
		if err == nil {
			err = errors.New("createBackup.sh produced an empty file")
		}
		return failAll("create", err)
	}
	log.Info("backup: created", "file", name, "size", size, "ms", time.Since(start).Milliseconds())
	run.Plain = &StagedFile{Name: name, Path: plainPath, SHA256: sum, Size: size}
	enabled := p.Crypt != nil && p.Crypt.Enabled()
	needAge, needPlain := false, false
	for _, t := range targets {
		if wantsAge(t, enabled) {
			needAge = true
		} else {
			needPlain = true
		}
	}
	if needAge {
		agePath := plainPath + ".age"
		asum, asize, err := encryptTo(p.Crypt, plainPath, agePath)
		if err != nil {
			return failAll("encrypt", fmt.Errorf("encryption: %w", err))
		}
		run.Age = &StagedFile{Name: name + ".age", Path: agePath, SHA256: asum, Size: asize, Encrypted: true}
		if !needPlain {
			_ = os.Remove(plainPath)
			run.Plain = nil
		}
	}
	for _, t := range targets {
		if !t.IsLocal() {
			continue
		}
		f := run.Plain
		if wantsAge(t, enabled) {
			f = run.Age
		}
		r := deliverLocal(ctx, t, *f, host, p.GID)
		r.Instance = instance
		if !r.OK && t.IsMount() && r.Step == "mount" {
			if s := p.mountLogState(t); s != "" {
				r.State = s
			}
		}
		p.writeResult(t, r)
		if r.OK {
			log.Info("backup: delivered", "target", t.ID, "kind", t.Kind, "file", r.Name, "ms", r.DurationMS, "removed", len(r.Removed))
		} else {
			failed = append(failed, t.ID)
			log.Warn("backup: delivery failed", "target", t.ID, "kind", t.Kind, "state", r.State, "step", r.Step, "err", r.Error)
		}
	}
	for _, f := range []*StagedFile{run.Plain, run.Age} {
		if f != nil {
			p.own(f.Path)
		}
	}
	writeRun()
	if len(failed) > 0 {
		return fmt.Errorf("delivery failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

// MountLog answers the mount unit's last journal lines (mount.nfs and mount.cifs say there what
// went wrong); nil = none.
var MountLog func(unit string) string

// shareMounted says whether a network share is mounted on where (a test swaps it).
var shareMounted = func(where string) bool {
	_, ok := shares.ReadMountinfo("/proc/self/mountinfo", where)
	return ok
}

// mountLogState is why the share's mount failed, from its unit's journal - only when it is not
// mounted: a mounted share's error (EACCES on a root-squashed export) is its own reason, and the
// journal's latest line is then systemd's "Mounted …" (B-212).
func (p *Pipeline) mountLogState(t Target) string {
	if MountLog == nil || shareMounted(netmount.Base+"/"+t.MountID()) {
		return ""
	}
	text := MountLog("media-net-" + t.MountID() + ".mount")
	if text == "" {
		return ""
	}
	st, _ := shares.MountReason(text)
	return st
}

// probe checks that a directory or share target takes a file: the checks the delivery starts with,
// its folder made, a small file written and removed - as root, the identity the delivery writes
// with. The result is the one the delivery would have written when it does not.
func (p *Pipeline) probe(t Target, at time.Time, instance string) (Result, bool) {
	step, err := probeDir(t)
	if err == nil {
		return Result{}, true
	}
	r := Result{At: at, Instance: instance, State: Classify(err), Step: step, Error: err.Error()}
	if t.IsMount() && step == "mount" {
		if s := p.mountLogState(t); s != "" {
			r.State = s
		}
	}
	if r.State == StateWritable || r.State == StateError && t.IsMount() && step == "mount" {
		r.State = StateUnreachable
	}
	return r, false
}

// probeDir is probe's filesystem half (a test swaps it).
var probeDir = func(t Target) (string, error) {
	if step, err := checkDir(t); err != nil {
		return step, err
	}
	dir := t.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "mkdir", err
	}
	f, err := os.CreateTemp(dir, ".occulite-probe-*")
	if err != nil {
		return "create", err
	}
	_, werr := f.Write([]byte("openccu-lite backup probe\n"))
	cerr := f.Close()
	_ = os.Remove(f.Name())
	if werr != nil {
		return "write", werr
	}
	if cerr != nil {
		return "write", cerr
	}
	return "", nil
}

// DeliverRun is the occulite half: the SFTP uploads, the record of the files handed out, and the
// staging directory emptied.
func (p *Pipeline) DeliverRun(ctx context.Context, instance string) error {
	log := p.log()
	if !ValidInstance(instance) {
		return fmt.Errorf("instance %q", instance)
	}
	b, err := os.ReadFile(filepath.Join(p.Staging, RunFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer p.cleanup()
	var run Run
	if err := json.Unmarshal(b, &run); err != nil {
		return fmt.Errorf("run.json: %w", err)
	}
	if run.Failed {
		return nil
	}
	targets, err := p.Select(instance)
	if err != nil && instance != run.Instance {
		return err
	}
	in := map[string]bool{}
	for _, id := range run.Targets {
		in[id] = true
	}
	enabled := run.Age != nil
	host := p.Hostname
	if host == "" {
		host = "openccu-lite"
	}
	var failed []string
	for _, t := range targets {
		if t.Kind != KindSFTP || !in[t.ID] {
			continue
		}
		f := run.Plain
		if wantsAge(t, enabled) && run.Age != nil {
			f = run.Age
		}
		if f == nil {
			continue
		}
		r := p.deliverSFTP(ctx, t, *f, host)
		r.Instance = instance
		p.writeResult(t, r)
		if r.OK {
			log.Info("backup: uploaded", "target", t.ID, "file", r.Name, "ms", r.DurationMS, "removed", len(r.Removed))
		} else {
			failed = append(failed, t.ID)
			log.Warn("backup: upload failed", "target", t.ID, "state", r.State, "step", r.Step, "err", r.Error)
		}
	}
	// B-99: the list is what tells a restore that this system made the file; a list that could
	// not be written (a full user partition) fails the deliver unit, so the journal and the
	// unit's state say so - the targets' results stay theirs, their files are delivered
	var listErr error
	if p.Crypt != nil {
		for _, f := range []*StagedFile{run.Plain, run.Age} {
			if f != nil {
				if err := p.Crypt.RecordCreated(backupcrypt.Created{SHA256: f.SHA256, Name: f.Name, Size: f.Size, Time: run.At, Encrypted: f.Encrypted}); err != nil {
					log.Warn("backup: created list not written", "err", err)
					listErr = fmt.Errorf("the list of backups this system made is not written: %w", err)
				}
			}
		}
	}
	if len(failed) > 0 {
		return errors.Join(fmt.Errorf("upload failed: %s", strings.Join(failed, ", ")), listErr)
	}
	return listErr
}

func (p *Pipeline) cleanup() {
	entries, err := os.ReadDir(p.Staging)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(p.Staging, e.Name()))
	}
}

// deliverSFTP uploads one file and applies retention.
func (p *Pipeline) deliverSFTP(ctx context.Context, t Target, f StagedFile, host string) Result {
	start := time.Now()
	r := Result{At: start, Name: f.Name, Encrypted: f.Encrypted}
	fail := func(step string, err error) Result {
		r.OK, r.Step, r.Error, r.State = false, step, err.Error(), Classify(err)
		r.DurationMS = time.Since(start).Milliseconds()
		return r
	}
	c, err := DialSFTP(ctx, t.SFTP, p.Store.SecretDir(t.ID))
	if err != nil {
		return fail("connect", err)
	}
	defer c.Close()
	dir := t.RemoteDir()
	if free, _, ok := c.Space(dir); ok && free > 0 && free < f.Size+f.Size/10 {
		r.State, r.Step, r.Error = StateFull, "space", fmt.Sprintf("%d bytes free, %d needed", free, f.Size+f.Size/10)
		r.DurationMS = time.Since(start).Milliseconds()
		return r
	}
	in, err := os.Open(f.Path)
	if err != nil {
		return fail("open", err)
	}
	defer in.Close()
	if err := c.Upload(dir, f.Name, in, f.Size); err != nil {
		return fail("upload", err)
	}
	r.OK, r.State, r.SHA256, r.Size = true, StateWritable, f.SHA256, f.Size
	if !t.AppendOnly {
		if files, err := c.List(dir); err == nil {
			for _, n := range Expired(files, host, t.MaxBackups) {
				if n == f.Name {
					continue
				}
				if err := c.Remove(dir, n); err == nil {
					r.Removed = append(r.Removed, n)
				}
			}
		}
	}
	r.DurationMS = time.Since(start).Milliseconds()
	return r
}
