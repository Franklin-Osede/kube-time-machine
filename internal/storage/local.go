package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
	"github.com/Franklin-Osede/kube-time-machine/pkg/types"
)

// Local persists snapshots to a directory on the local filesystem.
//
// On-disk layout:
//
//	<root>/
//	  index.json
//	  snapshots/
//	    <id>/
//	      meta.json
//	      full.json    -- iff meta.kind == "full"
//	      delta.json   -- iff meta.kind == "delta"
//
// index.json is a *cache* of the per-snapshot meta.json files. If the
// index goes missing, the store remains semantically intact — every
// snapshot is fully self-describing inside its own directory. A future
// rebuild routine can walk snapshots/ and regenerate the index without
// touching payloads. (The rebuild itself is not implemented yet; the
// format is designed to support it.)
type Local struct {
	root string

	lockMu   sync.Mutex
	lockFile *os.File

	mu    sync.Mutex
	index []types.SnapshotMeta // sorted by Timestamp ascending
}

const (
	indexFileName    = "index.json"
	snapshotsDirName = "snapshots"
	metaFileName     = "meta.json"
	fullFileName     = "full.json"
	deltaFileName    = "delta.json"
)

// Root returns the root directory path of the store. Useful in tests that
// need to inspect the on-disk layout without bypassing the Store interface.
func (l *Local) Root() string { return l.root }

// NewLocal opens (or initializes) a Local store rooted at the given path.
// The root and its snapshots/ subdirectory are created if missing. If
// index.json exists it is loaded; otherwise the in-memory index starts
// empty (and will be written on the next successful Put).
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(filepath.Join(root, snapshotsDirName), 0o755); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	l := &Local{root: root}
	if err := l.loadIndex(); err != nil {
		return nil, fmt.Errorf("storage: load index: %w", err)
	}
	return l, nil
}

// AcquireWriterLock obtains a process-scoped exclusive lock for this store.
//
// NewLocal deliberately does not acquire the lock: CLI read commands must be
// able to inspect a store while the agent is recording. The agent is the only
// writer process and calls this method during startup. A second agent pointed
// at the same PVC fails fast instead of racing writes to index.json and the
// snapshot directories.
func (l *Local) AcquireWriterLock() error {
	l.lockMu.Lock()
	defer l.lockMu.Unlock()

	if l.lockFile != nil {
		return nil
	}

	path := filepath.Join(l.root, ".writer.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("storage: open writer lock: %w", err)
	}
	if err := lockFileExclusive(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("storage: another process holds the writer lock on %s: %w", l.root, err)
	}
	l.lockFile = f
	return nil
}

// Close releases the writer lock, when held. Closing the file descriptor also
// releases the OS lock automatically if the process exits without calling
// Close, so stale lock files do not create stale locks.
func (l *Local) Close() error {
	l.lockMu.Lock()
	defer l.lockMu.Unlock()

	if l.lockFile == nil {
		return nil
	}
	f := l.lockFile
	l.lockFile = nil

	unlockErr := unlockFile(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return fmt.Errorf("storage: unlock writer lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("storage: close writer lock: %w", closeErr)
	}
	return nil
}

func (l *Local) loadIndex() error {
	b, err := os.ReadFile(filepath.Join(l.root, indexFileName))
	if errors.Is(err, os.ErrNotExist) {
		// index.json is a pure cache of the per-snapshot meta.json files
		// (ADR-0004). If it is missing we rebuild it from disk rather than
		// starting blind — otherwise `list` and `blame` would report no
		// history even though every snapshot is intact in snapshots/.
		return l.rebuildIndex()
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &l.index); err != nil {
		// The cache exists but is corrupt (truncated mid-write, garbled).
		// Same recovery as a missing cache: rebuild from the authoritative
		// per-snapshot meta.json files rather than refusing to open the
		// store. rebuildIndex re-persists a clean index.json, healing it.
		slog.Warn("storage: index.json is corrupt; rebuilding from snapshots", "err", err)
		l.index = nil
		return l.rebuildIndex()
	}
	return nil
}

// rebuildIndex reconstructs the in-memory index by scanning the meta.json
// of every directory under snapshots/, then best-effort re-writes
// index.json so the next open is a cheap read. Directories without a
// readable meta.json are skipped — an incomplete write leaves the rest of
// the history usable rather than failing the whole open. Called only when
// index.json is absent.
func (l *Local) rebuildIndex() error {
	dir := filepath.Join(l.root, snapshotsDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		l.index = nil
		return nil
	}
	if err != nil {
		return err
	}

	var rebuilt []types.SnapshotMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		snapDir := filepath.Join(dir, e.Name())
		var meta types.SnapshotMeta
		if err := readJSON(filepath.Join(snapDir, metaFileName), &meta); err != nil {
			continue // incomplete/unreadable snapshot dir — skip, keep the rest
		}
		if !snapshotPayloadPresent(snapDir, meta) {
			continue // meta without a valid payload — incomplete write, skip
		}
		rebuilt = append(rebuilt, meta)
	}
	sort.SliceStable(rebuilt, func(i, j int) bool {
		return rebuilt[i].Timestamp.Before(rebuilt[j].Timestamp)
	})
	l.index = rebuilt

	// Persist the rebuilt cache. A write failure is not fatal: the
	// in-memory index is already correct for this process, and the next
	// open will simply rebuild again.
	if len(rebuilt) > 0 {
		_ = l.writeIndexLocked()
	}
	return nil
}

func (l *Local) writeIndexLocked() error {
	return atomicWriteJSON(filepath.Join(l.root, indexFileName), l.index)
}

// PutFull persists a full reference snapshot.
func (l *Local) PutFull(_ context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error) {
	meta := types.SnapshotMeta{
		ID:        idFromTime(ts),
		Kind:      types.KindFull,
		Timestamp: ts.UTC(),
		Kinds:     kindsFromSnapshot(snap),
	}
	if err := l.writeSnapshot(meta, fullFileName, snapshotToWire(snap)); err != nil {
		return types.SnapshotMeta{}, err
	}
	result, err := l.appendIndex(meta)
	if err != nil {
		// Roll back the on-disk snapshot directory so a failed index write
		// does not leave an orphan that rebuildIndex would silently accept.
		_ = os.RemoveAll(filepath.Join(l.root, snapshotsDirName, string(meta.ID)))
		return types.SnapshotMeta{}, err
	}
	return result, nil
}

// PutDelta persists an incremental delta from prevID.
func (l *Local) PutDelta(_ context.Context, ts time.Time, prevID types.SnapshotID, d delta.Delta) (types.SnapshotMeta, error) {
	meta := types.SnapshotMeta{
		ID:        idFromTime(ts),
		Kind:      types.KindDelta,
		Timestamp: ts.UTC(),
		PrevID:    prevID,
		Kinds:     kindsFromDelta(d),
	}
	if err := l.writeSnapshot(meta, deltaFileName, deltaToWire(d)); err != nil {
		return types.SnapshotMeta{}, err
	}
	result, err := l.appendIndex(meta)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(l.root, snapshotsDirName, string(meta.ID)))
		return types.SnapshotMeta{}, err
	}
	return result, nil
}

// Get loads the payload at id from disk. The index is not consulted —
// per-snapshot meta.json is the source of truth.
func (l *Local) Get(_ context.Context, id types.SnapshotID) (Loaded, error) {
	dir := filepath.Join(l.root, snapshotsDirName, string(id))

	var meta types.SnapshotMeta
	if err := readJSON(filepath.Join(dir, metaFileName), &meta); err != nil {
		return Loaded{}, fmt.Errorf("storage: read meta %s: %w", id, err)
	}

	out := Loaded{Meta: meta}
	switch meta.Kind {
	case types.KindFull:
		var w wireSnapshot
		if err := readJSON(filepath.Join(dir, fullFileName), &w); err != nil {
			return Loaded{}, fmt.Errorf("storage: read full payload %s: %w", id, err)
		}
		out.Full = wireToSnapshot(w)
	case types.KindDelta:
		var w wireDelta
		if err := readJSON(filepath.Join(dir, deltaFileName), &w); err != nil {
			return Loaded{}, fmt.Errorf("storage: read delta payload %s: %w", id, err)
		}
		out.Delta = wireToDelta(w)
	default:
		return Loaded{}, fmt.Errorf("storage: unknown snapshot kind %q for %s", meta.Kind, id)
	}
	return out, nil
}

// Delete removes the snapshot directory and its index entry. It is
// idempotent: if id does not exist the call returns nil. The caller is
// responsible for ensuring no remaining delta references id as its PrevID.
func (l *Local) Delete(_ context.Context, id types.SnapshotID) error {
	dir := filepath.Join(l.root, snapshotsDirName, string(id))

	l.mu.Lock()
	defer l.mu.Unlock()

	// Remove the on-disk directory first (while holding the lock) so there
	// is no window where a concurrent List sees a stale index entry pointing
	// to a directory that has already been deleted. A crash after RemoveAll
	// but before writeIndexLocked leaves the directory gone; rebuildIndex
	// (called on next open) will not find it and will not include it in the
	// rebuilt index, so the store self-heals on restart.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("storage: delete snapshot dir %s: %w", id, err)
	}

	// Filter the in-memory index.
	filtered := l.index[:0]
	for _, m := range l.index {
		if m.ID != id {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == len(l.index) {
		// id was not in the index — nothing more to do.
		return nil
	}
	l.index = filtered

	if err := l.writeIndexLocked(); err != nil {
		return fmt.Errorf("storage: write index after delete: %w", err)
	}
	return nil
}

// List returns a copy of the in-memory index, sorted by Timestamp.
func (l *Local) List(_ context.Context) ([]types.SnapshotMeta, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]types.SnapshotMeta, len(l.index))
	copy(out, l.index)
	return out, nil
}

// writeSnapshot creates the per-snapshot directory and writes the payload
// before meta.json so a crash mid-write never leaves a meta-only dir that
// rebuildIndex would accept. Each file is written atomically (rename-based)
// with fsync for durability across node crashes.
func (l *Local) writeSnapshot(meta types.SnapshotMeta, payloadName string, payload any) error {
	dir := filepath.Join(l.root, snapshotsDirName, string(meta.ID))
	if err := os.Mkdir(dir, 0o755); errors.Is(err, os.ErrExist) {
		return fmt.Errorf("storage: snapshot ID collision at %s", meta.ID)
	} else if err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", meta.ID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := syncDir(filepath.Join(l.root, snapshotsDirName)); err != nil {
		return fmt.Errorf("storage: sync snapshots dir %s: %w", meta.ID, err)
	}
	if err := atomicWriteJSON(filepath.Join(dir, payloadName), payload); err != nil {
		return fmt.Errorf("storage: write payload %s: %w", meta.ID, err)
	}
	if err := atomicWriteJSON(filepath.Join(dir, metaFileName), meta); err != nil {
		return fmt.Errorf("storage: write meta %s: %w", meta.ID, err)
	}
	committed = true
	return nil
}

func (l *Local) appendIndex(meta types.SnapshotMeta) (types.SnapshotMeta, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Build the updated index in a fresh slice so a disk-write failure leaves
	// l.index entirely unchanged — no partial mutation to roll back.
	updated := make([]types.SnapshotMeta, len(l.index)+1)
	copy(updated, l.index)
	updated[len(l.index)] = meta
	sort.SliceStable(updated, func(i, j int) bool {
		return updated[i].Timestamp.Before(updated[j].Timestamp)
	})

	old := l.index
	l.index = updated
	if err := l.writeIndexLocked(); err != nil {
		l.index = old // restore: do not advance in-memory state on disk failure
		return types.SnapshotMeta{}, fmt.Errorf("storage: write index: %w", err)
	}
	return meta, nil
}

// idFromTime renders a UTC timestamp at millisecond precision into a
// sortable, filesystem-safe ID like "20260518T140530123Z".
func idFromTime(t time.Time) types.SnapshotID {
	t = t.UTC()
	ms := t.Nanosecond() / int(time.Millisecond)
	return types.SnapshotID(fmt.Sprintf("%s%03dZ", t.Format("20060102T150405"), ms))
}

// atomicWriteJSON marshals v as JSON and writes it to path via a
// tempfile-then-rename with fsync on the file and parent directory, so
// concurrent readers never see a partial file and committed data survives
// a node crash.
func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir fsyncs a directory so a rename into it is durable across crashes.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// snapshotPayloadPresent reports whether the on-disk payload for meta exists
// and is readable JSON. Used during index rebuild to skip incomplete writes.
func snapshotPayloadPresent(dir string, meta types.SnapshotMeta) bool {
	switch meta.Kind {
	case types.KindFull:
		var w wireSnapshot
		return readJSON(filepath.Join(dir, fullFileName), &w) == nil
	case types.KindDelta:
		var w wireDelta
		return readJSON(filepath.Join(dir, deltaFileName), &w) == nil
	default:
		return false
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// -- wire format --------------------------------------------------------
//
// JSON requires string map keys, but delta.Key is a struct. We serialize
// Snapshots and Deltas as ordered lists of entries instead. Sorting on
// write makes the on-disk bytes deterministic: two semantically equal
// snapshots produce byte-identical files, which keeps test fixtures
// stable and leaves the door open for content hashing / dedup later.

type wireEntry struct {
	Key   delta.Key   `json:"key"`
	State delta.State `json:"state"` // []byte → base64 string in JSON
}

type wireSnapshot struct {
	Entries []wireEntry `json:"entries"`
}

type wireDelta struct {
	Added    []wireEntry `json:"added,omitempty"`
	Modified []wireEntry `json:"modified,omitempty"`
	Removed  []delta.Key `json:"removed,omitempty"`
}

func snapshotToWire(s delta.Snapshot) wireSnapshot {
	out := wireSnapshot{Entries: make([]wireEntry, 0, len(s))}
	for k, v := range s {
		out.Entries = append(out.Entries, wireEntry{Key: k, State: v})
	}
	sortEntries(out.Entries)
	return out
}

func wireToSnapshot(w wireSnapshot) delta.Snapshot {
	out := make(delta.Snapshot, len(w.Entries))
	for _, e := range w.Entries {
		out[e.Key] = e.State
	}
	return out
}

func deltaToWire(d delta.Delta) wireDelta {
	out := wireDelta{}
	for k, v := range d.Added {
		out.Added = append(out.Added, wireEntry{Key: k, State: v})
	}
	for k, v := range d.Modified {
		out.Modified = append(out.Modified, wireEntry{Key: k, State: v})
	}
	for k := range d.Removed {
		out.Removed = append(out.Removed, k)
	}
	sortEntries(out.Added)
	sortEntries(out.Modified)
	sort.SliceStable(out.Removed, func(i, j int) bool { return keyLess(out.Removed[i], out.Removed[j]) })
	return out
}

func wireToDelta(w wireDelta) delta.Delta {
	d := delta.Delta{
		Added:    make(map[delta.Key]delta.State, len(w.Added)),
		Modified: make(map[delta.Key]delta.State, len(w.Modified)),
		Removed:  make(map[delta.Key]struct{}, len(w.Removed)),
	}
	for _, e := range w.Added {
		d.Added[e.Key] = e.State
	}
	for _, e := range w.Modified {
		d.Modified[e.Key] = e.State
	}
	for _, k := range w.Removed {
		d.Removed[k] = struct{}{}
	}
	return d
}

func sortEntries(es []wireEntry) {
	sort.SliceStable(es, func(i, j int) bool { return keyLess(es[i].Key, es[j].Key) })
}

func keyLess(a, b delta.Key) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

// -- kind index helpers --------------------------------------------------

// kindsFromSnapshot returns a sorted list of unique Kind strings present
// in the snapshot. Called during PutFull to populate SnapshotMeta.Kinds.
func kindsFromSnapshot(s delta.Snapshot) []string {
	seen := make(map[string]struct{}, 8)
	for k := range s {
		seen[k.Kind] = struct{}{}
	}
	return sortedStringSet(seen)
}

// kindsFromDelta returns a sorted list of unique Kind strings touched by
// the delta (added, modified, or removed entries). Called during PutDelta
// to populate SnapshotMeta.Kinds.
func kindsFromDelta(d delta.Delta) []string {
	seen := make(map[string]struct{}, 8)
	for k := range d.Added {
		seen[k.Kind] = struct{}{}
	}
	for k := range d.Modified {
		seen[k.Kind] = struct{}{}
	}
	for k := range d.Removed {
		seen[k.Kind] = struct{}{}
	}
	return sortedStringSet(seen)
}

// sortedStringSet converts a set-style map into a sorted slice of its keys.
func sortedStringSet(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
