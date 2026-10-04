package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/m1r3dk/dirclone/internal/model"
)

func TestOpenSchemaAndLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "dirclone.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var journal string
	if err = db.SQL().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode=%q", journal)
	}
	var fk int
	if err = db.SQL().QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys=%d", fk)
	}
	s := &model.Site{Name: "example", OriginalURL: "https://example.com/files", CanonicalURL: "https://example.com/files/", Hostname: "example.com"}
	if err = db.CreateSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s.ID == 0 || s.EntryCount != 1 || s.DirectoryCount != 1 {
		t.Fatalf("site not initialized: %+v", s)
	}
	root, err := db.EntryByPath(ctx, s.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	sz := int64(12)
	seen := time.Now().UTC()
	entries, err := db.UpsertEntries(ctx, []model.Entry{{SiteID: s.ID, ParentID: &root.ID, Name: "docs", NormalizedPath: "/docs", URL: "https://example.com/files/docs/", Type: model.EntryTypeDirectory, LastSeenAt: seen}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.UpsertEntries(ctx, []model.Entry{{SiteID: s.ID, ParentID: &entries[0].ID, Name: "readme.txt", NormalizedPath: "/docs/readme.txt", URL: "https://example.com/files/docs/readme.txt", Type: model.EntryTypeFile, Size: &sz, LastSeenAt: seen}})
	if err != nil {
		t.Fatal(err)
	}
	s, err = db.SiteByID(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.EntryCount != 3 || s.FileCount != 1 || s.DirectoryCount != 2 || s.TotalSize != 12 {
		t.Fatalf("bad aggregates: %+v", s)
	}
	if err = db.SetActiveSite(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveSite(ctx)
	if err != nil || active.ID != s.ID {
		t.Fatalf("active: %+v %v", active, err)
	}
	if err = db.SetSiteCWD(ctx, s.ID, "/docs"); err != nil {
		t.Fatal(err)
	}
	s, err = db.SiteByID(ctx, s.ID)
	if err != nil || s.CWD != "/docs" {
		t.Fatalf("cwd: %+v %v", s, err)
	}
	run := &model.CrawlRun{SiteID: s.ID, Mode: model.CrawlModeFull}
	if err = db.StartCrawlRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	ce := &model.CrawlError{RunID: run.ID, SiteID: s.ID, Path: "/bad", Message: "boom"}
	if err = db.AddCrawlError(ctx, ce); err != nil {
		t.Fatal(err)
	}
	run.Status = model.ScanStatusComplete
	run.Files = 1
	run.Directories = 2
	if err = db.FinishCrawlRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	entry, err := db.EntryByPath(ctx, s.ID, "/docs/readme.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	dl := &model.Download{SiteID: s.ID, EntryID: entry.ID, SourceURL: entry.URL, Destination: "/tmp/readme.txt", Status: model.DownloadRunning, BytesDone: 4, TotalBytes: &sz}
	if err = db.UpsertDownload(ctx, dl); err != nil {
		t.Fatal(err)
	}
	if dl.ID == 0 {
		t.Fatal("download ID not set")
	}
	if err = db.MarkEntriesRemovedBefore(ctx, s.ID, seen.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.EntryByPath(ctx, s.ID, "/docs/readme.txt", false); err == nil {
		t.Fatal("entry should be removed")
	}
}

func TestMemoryDatabasesAreIsolated(t *testing.T) {
	a, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := &model.Site{Name: "one", OriginalURL: "https://one.test/", CanonicalURL: "https://one.test/", Hostname: "one.test"}
	if err = a.CreateSite(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	sites, err := b.ListSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 0 {
		t.Fatalf("memory database leaked: %+v", sites)
	}
}
