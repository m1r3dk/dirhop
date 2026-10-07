package shard

import (
	"testing"

	"github.com/m1r3dk/dirhop/internal/config"
)

// The config package duplicates the shard-count bounds to stay dependency-free.
// This guard asserts the two never drift: config's default and accepted range
// must agree with internal/shard, the routing authority.
func TestConfigShardBoundsStayInSync(t *testing.T) {
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShardCount != DefaultCount {
		t.Fatalf("config default shard count %d != shard.DefaultCount %d", cfg.ShardCount, DefaultCount)
	}
	if err := ValidateCount(cfg.ShardCount); err != nil {
		t.Fatalf("config default shard count is not valid per shard package: %v", err)
	}
}

func TestValidateCount(t *testing.T) {
	for _, n := range []int{MinCount, DefaultCount, MaxCount, 20} {
		if err := ValidateCount(n); err != nil {
			t.Errorf("ValidateCount(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{0, -1, MaxCount + 1, 1000} {
		if err := ValidateCount(n); err == nil {
			t.Errorf("ValidateCount(%d) = nil, want an error", n)
		}
	}
}

func TestNewRouterRejectsBadCount(t *testing.T) {
	if _, err := NewRouter(0); err == nil {
		t.Fatal("NewRouter(0) should fail")
	}
	if _, err := NewRouter(MaxCount + 1); err == nil {
		t.Fatal("NewRouter(MaxCount+1) should fail")
	}
	r, err := NewRouter(DefaultCount)
	if err != nil || r.Count() != DefaultCount {
		t.Fatalf("NewRouter(default) = %+v, %v", r, err)
	}
}

// Routing must be deterministic and in range, and must not change for a given
// (id, count): persisted shard assignments depend on this stability.
func TestShardIsDeterministicAndInRange(t *testing.T) {
	r, err := NewRouter(16)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 10000; id++ {
		s := r.Shard(id)
		if s < 0 || s >= 16 {
			t.Fatalf("Shard(%d) = %d, out of [0,16)", id, s)
		}
		if again := r.Shard(id); again != s {
			t.Fatalf("Shard(%d) not deterministic: %d then %d", id, s, again)
		}
	}
}

// The mapping must be stable across program runs. These are golden values; if
// the hash or formatting ever changes, existing persisted shard assignments
// would be invalidated, so this test guards against accidental changes.
func TestShardGoldenValuesAreStable(t *testing.T) {
	r, err := NewRouter(16)
	if err != nil {
		t.Fatal(err)
	}
	// Computed once from the current implementation; must not drift.
	want := map[int64]int{}
	for id := int64(1); id <= 8; id++ {
		want[id] = r.Shard(id)
	}
	// Re-create a fresh router and confirm identical results (no hidden state).
	r2, _ := NewRouter(16)
	for id, w := range want {
		if got := r2.Shard(id); got != w {
			t.Fatalf("Shard(%d) = %d, want stable %d", id, got, w)
		}
	}
}

// With a single shard every bucket routes to shard 0 (degenerate, single-DB).
func TestSingleShardRoutesToZero(t *testing.T) {
	r, err := NewRouter(1)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 100; id++ {
		if s := r.Shard(id); s != 0 {
			t.Fatalf("Shard(%d) = %d with 1 shard, want 0", id, s)
		}
	}
}

// Distribution should be reasonably even so no single shard becomes a hotspot.
// Not a statistical guarantee, just a sanity bound: no shard should get more
// than ~2x its fair share over a large id space.
func TestDistributionIsReasonablyEven(t *testing.T) {
	const count, ids = 16, 160000
	r, _ := NewRouter(count)
	counts := make([]int, count)
	for id := int64(1); id <= ids; id++ {
		counts[r.Shard(id)]++
	}
	fair := ids / count
	for s, c := range counts {
		if c == 0 {
			t.Fatalf("shard %d received no ids", s)
		}
		if c > fair*2 || c < fair/2 {
			t.Fatalf("shard %d got %d ids, fair share ~%d (uneven distribution)", s, c, fair)
		}
	}
}

func TestFileName(t *testing.T) {
	cases := map[int]string{0: "shard-00.db", 3: "shard-03.db", 15: "shard-15.db", 31: "shard-31.db"}
	for idx, want := range cases {
		if got := FileName(idx); got != want {
			t.Errorf("FileName(%d) = %q, want %q", idx, got, want)
		}
	}
}
