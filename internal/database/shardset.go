package database

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/m1r3dk/dirhop/internal/shard"
)

// ShardSet owns the fixed set of entry-storage shard databases described in
// docs/adr/0001-sharded-storage.md. Each shard is an independent *DB (its own
// SQLite file and its own single writer), so scans routed to different shards
// write concurrently instead of serializing on one writer.
//
// Shards are opened lazily on first use and cached, so a process that only
// touches a few buckets never opens all shard files. The set is safe for
// concurrent use; opening is guarded, and the per-shard *DB handles carry their
// own write serialization.
type ShardSet struct {
	router      shard.Router
	dir         string
	busyTimeout time.Duration

	mu     sync.Mutex
	shards []*DB // index-aligned to shard indices; nil until first opened
}

// OpenShardSet prepares a shard set of count shards under dir. It validates the
// count and constructs the router but does not open any shard file yet; shards
// open on demand. dir is created by the caller (config.EnsureDirs).
func OpenShardSet(dir string, count int, busyTimeout time.Duration) (*ShardSet, error) {
	router, err := shard.NewRouter(count)
	if err != nil {
		return nil, err
	}
	if busyTimeout <= 0 {
		return nil, fmt.Errorf("busy timeout must be positive")
	}
	return &ShardSet{
		router:      router,
		dir:         dir,
		busyTimeout: busyTimeout,
		shards:      make([]*DB, count),
	}, nil
}

// Count returns the number of shards.
func (s *ShardSet) Count() int { return s.router.Count() }

// ShardIndex returns the shard index that owns siteID. Callers persist this on
// the catalog row so routing never has to be recomputed from a possibly-changed
// shard count.
func (s *ShardSet) ShardIndex(siteID int64) int { return s.router.Shard(siteID) }

// For returns the shard database that owns siteID, opening it on first use.
func (s *ShardSet) For(siteID int64) (*DB, error) {
	return s.ByIndex(s.router.Shard(siteID))
}

// ByIndex returns the shard database at index, opening it on first use. It is
// used by fan-out reads (global search/stats) that visit every shard.
func (s *ShardSet) ByIndex(index int) (*DB, error) {
	if index < 0 || index >= s.router.Count() {
		return nil, fmt.Errorf("shard index %d out of range [0, %d)", index, s.router.Count())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.shards[index]; db != nil {
		return db, nil
	}
	path := filepath.Join(s.dir, shard.FileName(index))
	db, err := OpenWithTimeout(path, s.busyTimeout)
	if err != nil {
		return nil, fmt.Errorf("open shard %d: %w", index, err)
	}
	s.shards[index] = db
	return db, nil
}

// Each visits every shard that has been opened or can be opened, in index
// order, calling fn with each shard's index and database. It stops at the first
// error. This is the sequential form of fan-out; callers that need parallel
// fan-out can range over indices and call ByIndex themselves.
func (s *ShardSet) Each(fn func(index int, db *DB) error) error {
	for i := 0; i < s.router.Count(); i++ {
		db, err := s.ByIndex(i)
		if err != nil {
			return err
		}
		if err := fn(i, db); err != nil {
			return err
		}
	}
	return nil
}

// OpenedShards returns how many shard databases are currently open. It exists
// for tests and diagnostics; lazy opening means this is often less than Count.
func (s *ShardSet) OpenedShards() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, db := range s.shards {
		if db != nil {
			n++
		}
	}
	return n
}

// Close closes every opened shard database. The first error is returned after
// attempting to close the rest, so one bad handle never leaks the others.
func (s *ShardSet) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for i, db := range s.shards {
		if db == nil {
			continue
		}
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.shards[i] = nil
	}
	return firstErr
}
