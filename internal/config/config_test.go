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
