// Package store is occulited's own database file (openccu-lite tasks 214, 194 and 195; D-113,
// D-114): one bbolt file for everything occulited keeps beyond its JSON state files - the health
// history of the Interfaces page (bucket "health", task 214), the state store (task 194, "state":
// keyed entries, not rings) and the datapoint history (task 195, "history"). occulited alone opens it; its path and its
// layout are no contract, every other program asks the API.
//
// bbolt is an ordered key/value store with buckets and transactions and nothing else: no TTL, no
// ring buffer, no downsampling. The ring is ours (WriteRings): one bucket per series, the key the
// row's time as 8 big-endian bytes (so a cursor walks it in order), the value whatever the series'
// owner encodes; an append and the delete of the rows past the ring's length happen in the same
// transaction, with FillPercent 1.0 because the keys only ever rise.
//
// The file lives in <state>/data/, a directory tagged .nobackup: createBackup.sh's
// --exclude-tag=.nobackup leaves it out of every backup - a history is not configuration (task
// 214, the maintainer's rule for logs). Not in /usr/local/tmp, which the boot empties (B-137).
//
// 32-bit boxes (armv7l): bbolt maps the whole file into the address space. The file is bounded
// by the ring's length times the series (500 rows of a few bytes each, a handful of series for
// the health history: tens of kilobytes), so this is far from the limit; the state store and the
// datapoint history keep the same bound.
package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

const (
	// DirName is the directory under occulited's state directory that holds the file.
	DirName = "data"
	// FileName is the database file in it.
	FileName = "occulited.db"
	// NoBackupTag is the tag file createBackup.sh's --exclude-tag looks for.
	NoBackupTag = ".nobackup"
	// DefaultRows is the ring's length, the same for every series (task 195's Q&A, 2026-09-24:
	// one N for everything, about 8 h of health history at a sample a minute).
	DefaultRows = 500
)

// FutureSlack is how far past the clock a history sample may lie before the samplers drop it
// (occulited B-75): a boot with a wrong clock - an RTC set years ahead, a test - stamps its samples
// in the future, and as the rows are keyed by time the ring's trim keeps exactly those and drops
// the real ones. The slack covers a clock that chrony steps back by a little.
const FutureSlack = 10 * time.Minute

// Future reports whether a sample's time t lies past now by more than FutureSlack.
func Future(t, now time.Time) bool { return t.After(now.Add(FutureSlack)) }

// Path is the database file under a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, DirName, FileName) }

// Row is one row of a series: its time (the key) and its encoded value.
type Row struct {
	At    time.Time
	Value []byte
}

// Series is the rows of one series to write: Bucket is the owner's top-level bucket (health,
// state, history), Name the series inside it. Replace drops what the file holds for the series
// first, so the file then holds exactly Rows (after an open, when memory is the whole truth).
type Series struct {
	Bucket  string
	Name    string
	Rows    []Row
	Replace bool
}

// DB is the open file.
type DB struct {
	bolt *bolt.DB
	path string
}

// ErrCorrupt wraps what made a file unreadable; Open moved that file aside and started anew.
var ErrCorrupt = errors.New("the database file was unreadable and was moved aside")

// Open opens (or creates) the file at path, its directory tagged .nobackup. A file bbolt cannot
// open - damaged, or not a bbolt file - is renamed to <path>.corrupt (one kept) and a fresh one
// is made: the history is lost, occulited is not. That case returns the open DB together with an
// error wrapping ErrCorrupt, for the caller's warning. Any other failure returns no DB.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tag := filepath.Join(dir, NoBackupTag)
	if _, err := os.Stat(tag); err != nil {
		if err := os.WriteFile(tag, nil, 0o600); err != nil {
			return nil, err
		}
	}
	db, err := openBolt(path)
	if err == nil {
		return &DB{bolt: db, path: path}, nil
	}
	if errors.Is(err, bolterrors.ErrTimeout) {
		return nil, fmt.Errorf("%s is held by another process: %w", path, err)
	}
	if _, serr := os.Stat(path); serr != nil {
		return nil, err // nothing to move aside: the directory itself is the problem
	}
	if rerr := os.Rename(path, path+".corrupt"); rerr != nil {
		return nil, fmt.Errorf("%w (and it could not be moved aside: %v)", err, rerr)
	}
	db, nerr := openBolt(path)
	if nerr != nil {
		return nil, nerr
	}
	return &DB{bolt: db, path: path}, fmt.Errorf("%w: %v", ErrCorrupt, err)
}

func openBolt(path string) (db *bolt.DB, err error) {
	// bbolt panics on some damaged pages instead of answering an error
	defer func() {
		if r := recover(); r != nil {
			db, err = nil, fmt.Errorf("bbolt: %v", r)
		}
	}()
	return bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
}

// Close closes the file.
func (d *DB) Close() error { return d.bolt.Close() }

// Path is the file's path.
func (d *DB) Path() string { return d.path }

// Size is the file's size in bytes (0 when it cannot be read).
func (d *DB) Size() int64 {
	fi, err := os.Stat(d.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// timeKey is a row's key: its time in nanoseconds since the epoch, 8 bytes big-endian, so the
// keys sort by time. A time before 1970 is clamped to 0.
func timeKey(t time.Time) []byte {
	n := t.UnixNano()
	if n < 0 {
		n = 0
	}
	var k [8]byte
	binary.BigEndian.PutUint64(k[:], uint64(n))
	return k[:]
}

func keyTime(k []byte) (time.Time, bool) {
	if len(k) != 8 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(binary.BigEndian.Uint64(k))), true
}

// guard turns a bbolt panic (a damaged page met on the way) into an error.
func guard(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("%w: %v", ErrCorrupt, r)
	}
}

// WriteRings writes the rows of every series in one transaction and trims each series to its
// newest n rows (n <= 0: DefaultRows). Rows with a time a series already holds overwrite it.
func (d *DB) WriteRings(series []Series, n int) (err error) {
	if n <= 0 {
		n = DefaultRows
	}
	defer guard(&err)
	return d.bolt.Update(func(tx *bolt.Tx) error { return putRings(tx, series, n) })
}

func putRings(tx *bolt.Tx, series []Series, n int) error {
	for _, s := range series {
		top, err := tx.CreateBucketIfNotExists([]byte(s.Bucket))
		if err != nil {
			return err
		}
		if s.Replace && top.Bucket([]byte(s.Name)) != nil {
			if err := top.DeleteBucket([]byte(s.Name)); err != nil {
				return err
			}
		}
		b, err := top.CreateBucketIfNotExists([]byte(s.Name))
		if err != nil {
			return err
		}
		b.FillPercent = 1.0
		for _, r := range s.Rows {
			if err := b.Put(timeKey(r.At), r.Value); err != nil {
				return err
			}
		}
		if err := trim(b, n); err != nil {
			return err
		}
	}
	return nil
}

// trim deletes every row of b but the newest n.
func trim(b *bolt.Bucket, n int) error {
	c := b.Cursor()
	k, _ := c.Last()
	for i := 1; i < n && k != nil; i++ {
		k, _ = c.Prev()
	}
	if k == nil {
		return nil // n rows or fewer
	}
	var old [][]byte
	for k, _ = c.Prev(); k != nil; k, _ = c.Prev() {
		old = append(old, append([]byte(nil), k...))
	}
	for _, k := range old {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// Entry is one keyed value of a keeper that keeps entries rather than rings (the state store,
// task 194): Bucket is the keeper's top-level bucket, Group a bucket inside it (the interface),
// Key the entry's key in that; a nil Value deletes the entry.
type Entry struct {
	Bucket, Group, Key string
	Value              []byte
}

// Write commits the rows of every series (trimmed to n as WriteRings does) and the entries in
// one transaction: one commit, one fsync, whatever the keepers handed over.
func (d *DB) Write(series []Series, entries []Entry, n int) (err error) {
	if n <= 0 {
		n = DefaultRows
	}
	defer guard(&err)
	return d.bolt.Update(func(tx *bolt.Tx) error {
		if err := putRings(tx, series, n); err != nil {
			return err
		}
		return putEntries(tx, entries)
	})
}

func putEntries(tx *bolt.Tx, entries []Entry) error {
	for _, e := range entries {
		top, err := tx.CreateBucketIfNotExists([]byte(e.Bucket))
		if err != nil {
			return err
		}
		if e.Value == nil {
			if b := top.Bucket([]byte(e.Group)); b != nil {
				if err := b.Delete([]byte(e.Key)); err != nil {
					return err
				}
			}
			continue
		}
		b, err := top.CreateBucketIfNotExists([]byte(e.Group))
		if err != nil {
			return err
		}
		if err := b.Put([]byte(e.Key), e.Value); err != nil {
			return err
		}
	}
	return nil
}

// ReadEntries returns every entry under a top-level bucket by group and key; an absent bucket is
// an empty answer.
func (d *DB) ReadEntries(bucket string) (out map[string]map[string][]byte, err error) {
	out = map[string]map[string][]byte{}
	defer guard(&err)
	err = d.bolt.View(func(tx *bolt.Tx) error {
		top := tx.Bucket([]byte(bucket))
		if top == nil {
			return nil
		}
		return top.ForEachBucket(func(name []byte) error {
			g := map[string][]byte{}
			if err := top.Bucket(name).ForEach(func(k, v []byte) error {
				if v != nil {
					g[string(k)] = append([]byte(nil), v...)
				}
				return nil
			}); err != nil {
				return err
			}
			out[string(name)] = g
			return nil
		})
	})
	return out, err
}

// ReadRings returns every series under a top-level bucket, each oldest first; an absent bucket
// is an empty answer. Keys that are not times are skipped.
func (d *DB) ReadRings(bucket string) (out map[string][]Row, err error) {
	out = map[string][]Row{}
	defer guard(&err)
	err = d.bolt.View(func(tx *bolt.Tx) error {
		top := tx.Bucket([]byte(bucket))
		if top == nil {
			return nil
		}
		return top.ForEachBucket(func(name []byte) error {
			b := top.Bucket(name)
			var rows []Row
			if err := b.ForEach(func(k, v []byte) error {
				if t, ok := keyTime(k); ok {
					rows = append(rows, Row{At: t, Value: append([]byte(nil), v...)})
				}
				return nil
			}); err != nil {
				return err
			}
			out[string(name)] = rows
			return nil
		})
	})
	return out, err
}
