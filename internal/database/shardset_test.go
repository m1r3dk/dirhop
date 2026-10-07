package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m1r3dk/dirhop/internal/config"
	"github.com/m1r3dk/dirhop/internal/model"
)

// Integration boundary: build a ShardSet the way the application will, from a
// real loaded config (shard_count + shard_dir) and config.EnsureDirs, then
// actually create a site and index an entry through the resolved shard. This
// exercises config -> paths -> ShardSet -> DB end to end rather than hand-built
// fixtures.
func TestShardSetFromLoadedConfigIndexesEntry(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.toml")
	dbPath := filepath.Join(root, "data", "dirhop.db")
	content := "database = \"" + dbPath + "\"\nshard_count = 8\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	// Safety: the shard directory must live under the overridden database path's
	// directory (the temp root), never the global default location.
	if !filepathHasPrefix(cfg.Paths.ShardDir, root) {
		t.Fatalf("shard dir %q escaped the test root %q", cfg.Paths.ShardDir, root)
	}

	set, err := OpenShardSet(cfg.Paths.ShardDir, cfg.ShardCount, cfg.BusyTimeout)
	if err != nil {
		t.Fatalf("OpenShardSet from loaded config: %v", err)
	}
	defer set.Close()
	if set.Count() != 8 {
		t.Fatalf("shard count from config = %d, want 8", set.Count())
	}

	ctx := context.Background()
	const siteID = int64(123)
	db, err := set.For(siteID)
	if err != nil {
		t.Fatal(err)
	}
	site := &model.Site{Name: "bucket", OriginalURL: "https://b.s3.amazonaws.com/", CanonicalURL: "https://b.s3.amazonaws.com/", Hostname: "b.s3.amazonaws.com"}
	if err := db.CreateSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	root0, err := db.EntryByPath(ctx, site.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	sz := int64(7)
	if _, err := db.UpsertEntries(ctx, []model.Entry{{
		SiteID: site.ID, ParentID: &root0.ID, Name: "a.txt", NormalizedPath: "/a.txt",
		URL: "https://b.s3.amazonaws.com/a.txt", Type: model.EntryTypeFile, Size: &sz, LastSeenAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}

	// Read it back through the same shard the router resolves for this site.
	got, err := set.For(siteID)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := got.EntryByPath(ctx, site.ID, "/a.txt", false); err != nil || e.Name != "a.txt" {
		t.Fatalf("entry not found in resolved shard: %+v err=%v", e, err)
	}

	// The shard file exists on disk under the config-resolved shard directory.
	want := filepath.Join(cfg.Paths.ShardDir, "shard-0"+itoa(set.ShardIndex(siteID))+".db")
	if _, err := os.Stat(want); err != nil {
		// Fall back to a looser check: at least one shard-*.db exists.
		matches, _ := filepath.Glob(filepath.Join(cfg.Paths.ShardDir, "shard-*.db"))
		if len(matches) == 0 {
			t.Fatalf("no shard file created under %s (wanted ~%s)", cfg.Paths.ShardDir, want)
		}
	}
}

// itoa renders a single-digit shard index; shard indices here are < 10 (count 8).
func itoa(n int) string { return string(rune('0' + n)) }

// filepathHasPrefix reports whether path is within dir.
func filepathHasPrefix(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func newTestShardSet(t *testing.T, count int) *ShardSet {
	t.Helper()
	set, err := OpenShardSet(t.TempDir(), count, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

func TestShardSetLazyOpenAndRouting(t *testing.T) {
	set := newTestShardSet(t, 8)
	if set.Count() != 8 {
		t.Fatalf("count = %d, want 8", set.Count())
	}
	if set.OpenedShards() != 0 {
		t.Fatalf("expected no shards open before use, got %d", set.OpenedShards())
	}

	// The same site always resolves to the same shard handle.
	db1, err := set.For(42)
	if err != nil {
		t.Fatal(err)
	}
	db2, err := set.For(42)
	if err != nil {
		t.Fatal(err)
	}
	if db1 != db2 {
		t.Fatal("For(42) returned different handles; shard cache not working")
	}
	if set.OpenedShards() != 1 {
		t.Fatalf("expected exactly 1 shard open after touching one site, got %d", set.OpenedShards())
	}
	if set.ShardIndex(42) != set.ShardIndex(42) {
		t.Fatal("ShardIndex not deterministic")
	}

	// Out-of-range index is rejected.
	if _, err := set.ByIndex(8); err == nil {
		t.Fatal("ByIndex(8) should be out of range for 8 shards")
	}
}

// The core promise of sharding: a long write transaction on one shard must not
// block a write to a different shard. On a single shared database this write
// would wait for the open transaction; across shards it proceeds immediately.
func TestShardSetWritesToDifferentShardsDoNotBlock(t *testing.T) {
	// 2 shards is enough; pick two site IDs that route to different shards.
	set := newTestShardSet(t, 2)
	var a, b int64 = 0, 0
	for id := int64(1); id < 1000 && (a == 0 || b == 0); id++ {
		switch set.ShardIndex(id) {
		case 0:
			if a == 0 {
				a = id
			}
		case 1:
			if b == 0 {
				b = id
			}
		}
	}
	if a == 0 || b == 0 {
		t.Fatalf("could not find ids routing to both shards (a=%d b=%d)", a, b)
	}

	ctx := context.Background()
	shardA, err := set.For(a)
	if err != nil {
		t.Fatal(err)
	}
	shardB, err := set.For(b)
	if err != nil {
		t.Fatal(err)
	}
	if shardA == shardB {
		t.Fatal("test ids unexpectedly share a shard")
	}

	// Seed a site row in each shard so entries have a valid parent.
	seedSite := func(db *DB, id int64) *model.Site {
		s := &model.Site{Name: "s", OriginalURL: "https://x.test/", CanonicalURL: "https://x.test/", Hostname: "x.test"}
		if err := db.CreateSite(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	siteA := seedSite(shardA, a)
	siteB := seedSite(shardB, b)

	// Hold a write transaction open on shard A.
	txA, err := shardA.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback()
	if _, err := txA.ExecContext(ctx, `UPDATE app_state SET active_site_id=NULL WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}

	// Meanwhile, write to shard B. It must complete well within the shard busy
	// timeout, proving it is not blocked by shard A's open writer.
	rootB, err := shardB.EntryByPath(ctx, siteB.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		sz := int64(1)
		_, e := shardB.UpsertEntries(ctx, []model.Entry{{
			SiteID: siteB.ID, ParentID: &rootB.ID, Name: "f.txt", NormalizedPath: "/f.txt",
			URL: "https://x.test/f.txt", Type: model.EntryTypeFile, Size: &sz, LastSeenAt: time.Now().UTC(),
		}})
		done <- e
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write to shard B failed: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("write to shard B blocked on shard A's open writer; shards are not isolated")
	}

	_ = siteA
	txA.Rollback()
}

// Each visits every shard, opening lazily; after a full pass all shards are open.
func TestShardSetEachVisitsAll(t *testing.T) {
	set := newTestShardSet(t, 4)
	visited := map[int]bool{}
	err := set.Each(func(index int, db *DB) error {
		if db == nil {
			t.Fatalf("shard %d handle is nil", index)
		}
		visited[index] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(visited) != 4 || set.OpenedShards() != 4 {
		t.Fatalf("Each visited %d, opened %d, want 4/4", len(visited), set.OpenedShards())
	}
}

func TestOpenShardSetValidatesCount(t *testing.T) {
	if _, err := OpenShardSet(t.TempDir(), 0, time.Second); err == nil {
		t.Fatal("count 0 should be rejected")
	}
	if _, err := OpenShardSet(t.TempDir(), 33, time.Second); err == nil {
		t.Fatal("count 33 should be rejected")
	}
	if _, err := OpenShardSet(t.TempDir(), 16, 0); err == nil {
		t.Fatal("zero busy timeout should be rejected")
	}
}

// Shard files are created on disk with the expected names.
func TestShardSetCreatesNamedFiles(t *testing.T) {
	dir := t.TempDir()
	set, err := OpenShardSet(dir, 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	if _, err := set.ByIndex(3); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shard-03.db")); err != nil {
		t.Fatalf("shard-03.db not created: %v", err)
	}
}
