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
	"strings"
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
// touching payloads. (See rebuildIndex, which implements exactly that; the
// format is designed to support it.)
type Local struct {
	root string

	lockMu   sync.Mutex
	lockFile *os.File

	mu    sync.Mutex
	index []types.SnapshotMeta // sorted by Timestamp ascending

	// readOnly stores are opened by the CLI against a directory the agent may
	// be writing concurrently. They must never touch the filesystem: see
	// OpenForRead.
	readOnly bool
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

// NewLocal opens (or initializes) a Local store for WRITING, rooted at the
// given path. The root and its snapshots/ subdirectory are created if missing,
// index.json is loaded or rebuilt, and any deletion staged but not committed
// before a crash is reconciled.
//
// Opening for write repairs the store, which means writing to it. Only the
// agent may do that, and only while holding the writer lock — use OpenForRead
// for anything that merely inspects history.
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(filepath.Join(root, snapshotsDirName), 0o700); err != nil {
		return nil, fmt.Errorf("storage: create root: %w", err)
	}
	l := &Local{root: root}
	if err := l.loadIndex(); err != nil {
		return nil, fmt.Errorf("storage: load index: %w", err)
	}
	return l, nil
}

// OpenForRead opens a store for inspection without writing to it.
//
// This exists because opening for write is a repair operation: loadIndex
// rebuilds a missing or corrupt index.json and re-persists it, and
// reconcileDeletions rewrites the index and removes staged tombstones. Those
// are correct for the agent, which holds the writer lock. Running them from
// `ktm list` against a live PVC is not: the CLI takes no lock, so it would
// race the agent on index.json and could delete a tombstone for a deletion the
// agent had staged but not yet committed -- destroying the very evidence that
// makes the staged deletion recoverable.
//
// A read-only store therefore performs the same recovery IN MEMORY and touches
// nothing. It sees exactly what a repaired store would, and leaves the repair
// to the writer.
func OpenForRead(root string) (*Local, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("storage: open %s for reading: %w", root, err)
	}
	l := &Local{root: root, readOnly: true}
	if err := l.loadIndex(); err != nil {
		return nil, fmt.Errorf("storage: load index: %w", err)
	}
	return l, nil
}

// ErrReadOnly is returned by any mutating operation on a store opened with
// OpenForRead. It is a programming error rather than a runtime condition: the
// CLI opens read-only precisely so a write cannot be reached by accident.
var ErrReadOnly = errors.New("storage: store is open read-only")

// AcquireWriterLock obtains a process-scoped exclusive lock for this store.
//
// NewLocal deliberately does not acquire the lock: CLI read commands must be
// able to inspect a store while the agent is recording. The agent is the only
// writer process and calls this method during startup. A second agent pointed
// at the same PVC fails fast instead of racing writes to index.json and the
// snapshot directories.
func (l *Local) AcquireWriterLock() error {
	if l.readOnly {
		return ErrReadOnly
	}
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
		if err := l.rebuildIndex(); err != nil {
			return err
		}
		return l.reconcileDeletions()
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
		if err := l.rebuildIndex(); err != nil {
			return err
		}
		return l.reconcileDeletions()
	}
	return l.reconcileDeletions()
}

// reconcileDeletions completes or discards deletions that were staged under
// .deleting/ but never committed, and is the reason Delete's staging step is
// crash-safe. Called only from loadIndex, i.e. from NewLocal before the store
// is published to any other goroutine — hence writeIndexLocked is safe here
// despite l.mu not being held.
func (l *Local) reconcileDeletions() error {
	deletingDir := filepath.Join(l.root, ".deleting")
	entries, err := os.ReadDir(deletingDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	deleting := make(map[types.SnapshotID]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			deleting[types.SnapshotID(entry.Name())] = struct{}{}
		}
	}
	before := l.index
	filtered := make([]types.SnapshotMeta, 0, len(l.index))
	for _, meta := range l.index {
		if _, ok := deleting[meta.ID]; !ok {
			filtered = append(filtered, meta)
		}
	}
	l.index = filtered

	// A read-only store stops here: it has the same view a repaired store
	// would have, without having repaired anything. Persisting the filtered
	// index or clearing the tombstones is the writer's job.
	if l.readOnly {
		return nil
	}
	if len(filtered) != len(before) {
		if err := l.writeIndexLocked(); err != nil {
			return fmt.Errorf("storage: reconcile staged deletions: %w", err)
		}
	}
	for id := range deleting {
		if err := os.RemoveAll(filepath.Join(deletingDir, string(id))); err != nil {
			return fmt.Errorf("storage: clean staged deletion %s: %w", id, err)
		}
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
		if string(meta.ID) != e.Name() {
			continue // directory name disagrees with meta.ID — not a trustworthy snapshot
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
		if !l.readOnly {
			// Best-effort: a rebuilt index makes the next open a cheap read. A
			// read-only store skips it -- it has no lock, and the writer will
			// persist its own rebuild.
			_ = l.writeIndexLocked()
		}
	}
	return nil
}

func (l *Local) writeIndexLocked() error {
	return atomicWriteJSON(filepath.Join(l.root, indexFileName), l.index)
}

// PutFull persists a full reference snapshot.
func (l *Local) PutFull(_ context.Context, ts time.Time, snap delta.Snapshot) (types.SnapshotMeta, error) {
	if l.readOnly {
		return types.SnapshotMeta{}, ErrReadOnly
	}
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
	if l.readOnly {
		return types.SnapshotMeta{}, ErrReadOnly
	}
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
	dir, err := l.snapshotDir(id)
	if err != nil {
		return Loaded{}, err
	}

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
	if l.readOnly {
		return ErrReadOnly
	}
	dir, err := l.snapshotDir(id)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	filtered := make([]types.SnapshotMeta, 0, len(l.index))
	for _, m := range l.index {
		if m.ID != id {
			filtered = append(filtered, m)
		}
	}
	if len(filtered) == len(l.index) {
		return os.RemoveAll(dir) // idempotent; also cleans an unindexed orphan
	}

	// Stage the deletion: rename the directory out of the authoritative
	// snapshots namespace, then commit by writing the index, then discard the
	// tombstone. The rename is the point of no return for readers, and the
	// index write is the commit point for the record.
	//
	// A crash between the rename and the index write leaves index.json still
	// listing the snapshot while its directory sits under .deleting/. That is
	// exactly what reconcileDeletions repairs on the next open: it treats the
	// presence of .deleting/<id> as proof the deletion was intended, drops the
	// entry, and re-persists the index. rebuildIndex cannot resurrect the
	// snapshot either, because it scans snapshots/ and the tombstone is no
	// longer there.
	deletingDir := filepath.Join(l.root, ".deleting")
	if err := os.MkdirAll(deletingDir, 0o700); err != nil {
		return fmt.Errorf("storage: create deletion staging directory: %w", err)
	}
	tombstone := filepath.Join(deletingDir, string(id))
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("storage: remove stale deletion tombstone %s: %w", id, err)
	}
	if err := os.Rename(dir, tombstone); err != nil {
		return fmt.Errorf("storage: stage snapshot deletion %s: %w", id, err)
	}

	old := l.index
	l.index = filtered
	if err := l.writeIndexLocked(); err != nil {
		l.index = old
		if restoreErr := os.Rename(tombstone, dir); restoreErr != nil {
			return fmt.Errorf("storage: write index after delete: %w (restore snapshot: %v)", err, restoreErr)
		}
		return fmt.Errorf("storage: write index after delete: %w", err)
	}
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("storage: remove deleted snapshot %s: %w", id, err)
	}
	return syncDir(filepath.Join(l.root, snapshotsDirName))
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
	dir, err := l.snapshotDir(meta.ID)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); errors.Is(err, os.ErrExist) {
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

func (l *Local) snapshotDir(id types.SnapshotID) (string, error) {
	s := string(id)
	if s == "" || s == "." || s == ".." || filepath.IsAbs(s) || strings.ContainsAny(s, `/\`) || filepath.Base(s) != s {
		return "", fmt.Errorf("storage: invalid snapshot ID %q", id)
	}
	return filepath.Join(l.root, snapshotsDirName, s), nil
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
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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
