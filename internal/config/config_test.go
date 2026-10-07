package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultAndLoad(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Paths.Database == "" || cfg.CrawlConcurrency <= 0 || cfg.BusyTimeout <= 0 {
		t.Fatalf("invalid defaults: %+v", cfg)
	}
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("crawl_concurrency=3\ndownload_workers=2\nhttp_timeout=9s\nbusy_timeout=750ms\ndatabase=/tmp/custom.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CrawlConcurrency != 3 || cfg.DownloadWorkers != 2 || cfg.HTTPTimeout != 9*time.Second || cfg.BusyTimeout != 750*time.Millisecond {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Paths.Database != "/tmp/custom.db" {
		t.Fatalf("database = %q", cfg.Paths.Database)
	}
}

func TestLoadRejectsUnknownAndEnsureDirs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(file, []byte("typo=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(file); err == nil {
		t.Fatal("expected unknown key error")
	}
	root := t.TempDir()
	p := Paths{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), CacheDir: filepath.Join(root, "cache"), Database: filepath.Join(root, "db", "x.db"), History: filepath.Join(root, "state", "history")}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{p.DataDir, p.ConfigDir, p.CacheDir, filepath.Dir(p.Database), filepath.Dir(p.History)} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("directory %q not created", dir)
		}
	}
}

func TestLoadTOMLSyntax(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	content := `# dirhop
[crawl]
workers = 12          # alias for crawl_concurrency
timeout = "45s"
[download]
download_directory = '/tmp/dl # not a comment'
user_agent = "dirhop \"test\""
metadata = "full"
color = false
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CrawlConcurrency != 12 || cfg.HTTPTimeout != 45*time.Second || cfg.DownloadDirectory != "/tmp/dl # not a comment" ||
		cfg.UserAgent != `dirhop "test"` || cfg.Metadata != "full" || cfg.Color {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

// The shard count defaults to 16, is parseable, and is range-checked. ShardDir
// defaults under the database directory.
func TestShardCountConfig(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShardCount != 16 {
		t.Fatalf("default shard count = %d, want 16", cfg.ShardCount)
	}
	if cfg.Paths.ShardDir == "" {
		t.Fatal("default ShardDir is empty")
	}

	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("shard_count=32\nshard_dir=/tmp/myshards\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ShardCount != 32 {
		t.Fatalf("shard_count = %d, want 32", cfg.ShardCount)
	}
	if cfg.Paths.ShardDir != "/tmp/myshards" {
		t.Fatalf("shard_dir = %q", cfg.Paths.ShardDir)
	}

	// Out-of-range is rejected.
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("shard_count=33\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("shard_count=33 should be rejected")
	}
}

// When `database` is overridden but `shard_dir` is not, the shard directory must
// follow the database directory, not fall back to the global default location.
// Regression: a custom database path used to silently keep the default shards
// directory, so tests and alternate installs wrote into the shared location.
func TestShardDirFollowsDatabaseOverride(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sub", "custom.db")
	file := filepath.Join(dir, "config")
	if err := os.WriteFile(file, []byte("database = \""+dbPath+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(dbPath), "shards")
	if cfg.Paths.ShardDir != want {
		t.Fatalf("shard dir = %q, want %q (beside the overridden database)", cfg.Paths.ShardDir, want)
	}

	// An explicit shard_dir still wins.
	file2 := filepath.Join(dir, "config2")
	if err := os.WriteFile(file2, []byte("database = \""+dbPath+"\"\nshard_dir = \"/tmp/explicit\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(file2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Paths.ShardDir != "/tmp/explicit" {
		t.Fatalf("explicit shard_dir not honored: %q", cfg2.Paths.ShardDir)
	}
}

// EnsureDirs must create the shard directory too.
func TestEnsureDirsCreatesShardDir(t *testing.T) {
	root := t.TempDir()
	p := Paths{
		DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"),
		CacheDir: filepath.Join(root, "cache"), Database: filepath.Join(root, "db", "x.db"),
		ShardDir: filepath.Join(root, "db", "shards"), History: filepath.Join(root, "state", "history"),
	}
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p.ShardDir); err != nil || !st.IsDir() {
		t.Fatalf("shard dir %q not created: %v", p.ShardDir, err)
	}
}
