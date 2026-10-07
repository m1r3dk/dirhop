// Package shard defines how indexed buckets are distributed across a fixed set
// of storage shards. It is a small, pure, dependency-free layer: it decides
// which shard a bucket's entries belong to and how shard files are named. The
// actual databases are owned by higher layers; keeping routing isolated makes
// it trivially testable and stable over time.
//
// See docs/adr/0001-sharded-storage.md for the rationale.
package shard

import (
	"fmt"
	"hash/fnv"
)

const (
	// MinCount is the smallest legal shard count. A count of 1 degenerates to a
	// single entry database and is useful for tests and tiny installs.
	MinCount = 1
	// MaxCount bounds the shard count so global fan-out reads (which open every
	// shard) stay cheap. 32 comfortably covers the targeted scan parallelism.
	MaxCount = 32
	// DefaultCount matches typical scan parallelism (16-20 threads) while
	// keeping global reads inexpensive.
	DefaultCount = 16
)

// ValidateCount reports whether n is a usable shard count.
func ValidateCount(n int) error {
	if n < MinCount || n > MaxCount {
		return fmt.Errorf("shard count %d out of range [%d, %d]", n, MinCount, MaxCount)
	}
	return nil
}

// Router maps a site (bucket/listing) identifier to one of count shards. The
// mapping is deterministic and stable: the same id and count always yield the
// same shard, across processes and program versions, because it relies only on
// a fixed hash. Callers persist the resulting shard index on the catalog row so
// that a later change to count never silently remaps existing data.
type Router struct {
	count int
}

// NewRouter returns a Router over count shards, or an error if count is out of
// range. Validating at construction means the rest of the program can treat a
// Router as always valid.
func NewRouter(count int) (Router, error) {
	if err := ValidateCount(count); err != nil {
		return Router{}, err
	}
	return Router{count: count}, nil
}

// Count returns the number of shards this router distributes over.
func (r Router) Count() int { return r.count }

// Shard returns the shard index in [0, Count) that owns siteID. The hash is
// FNV-1a over the decimal id; its only requirement is a stable, well-spread
// distribution, not cryptographic strength. IDs are assigned by an
// autoincrement, so even a trivial modulus would spread them, but hashing keeps
// the distribution even if IDs become sparse (e.g. after deletes).
func (r Router) Shard(siteID int64) int {
	h := fnv.New64a()
	// Writing the fixed-width decimal of the id keeps the mapping independent of
	// platform integer size and of any future id formatting.
	_, _ = fmt.Fprintf(h, "%d", siteID)
	return int(h.Sum64() % uint64(r.count))
}

// FileName returns the on-disk file name for a shard index, e.g. "shard-03.db".
// Zero-padding to two digits keeps names sorted and aligned for the supported
// range (up to 32 shards).
func FileName(index int) string {
	return fmt.Sprintf("shard-%02d.db", index)
}
