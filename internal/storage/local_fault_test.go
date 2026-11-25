package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Franklin-Osede/kube-time-machine/internal/delta"
)

func TestPutFull_PayloadExistsBeforeMetadataWrite(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	writeErr := errors.New("metadata write failed")

	store.writeJSON = func(path string, value any) error {
		if filepath.Base(path) != metaFileName {
			return atomicWriteJSON(path, value)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), fullFileName)); err != nil {
			t.Errorf("payload missing at metadata write: %v", err)
		}
		reader, err := OpenForRead(root)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := reader.List(ctx)
		if err != nil || len(entries) != 0 {
			t.Errorf("before metadata commit: List = %v, %v; want empty history", entries, err)
		}
		return writeErr
	}

	if _, err := store.PutFull(ctx, time.Now(), delta.Snapshot{}); !errors.Is(err, writeErr) {
		t.Fatalf("PutFull returned %v, want %v", err, writeErr)
	}
}

func TestDelete_StagesSnapshotBeforeIndexWrite(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	meta, err := store.PutFull(ctx, time.Now(), delta.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("index write failed")

	store.writeJSON = func(path string, value any) error {
		if filepath.Base(path) != indexFileName {
			return atomicWriteJSON(path, value)
		}
		tombstone := filepath.Join(root, ".deleting", string(meta.ID), metaFileName)
		if _, err := os.Stat(tombstone); err != nil {
			t.Errorf("snapshot missing from tombstone at index write: %v", err)
		}
		active := filepath.Join(root, snapshotsDirName, string(meta.ID))
		if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("active snapshot at index write: stat returned %v, want missing", err)
		}
		return writeErr
	}

	if err := store.Delete(ctx, meta.ID); !errors.Is(err, writeErr) {
		t.Fatalf("Delete returned %v, want %v", err, writeErr)
	}
	if _, err := store.Get(ctx, meta.ID); err != nil {
		t.Fatalf("snapshot was not restored after index failure: %v", err)
	}
}
