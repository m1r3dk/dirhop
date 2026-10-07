package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/m1r3dk/dirhop/internal/model"
)

func TestOpenSchemaAndLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "dirhop.db"))
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

func TestConcurrentHandlesWaitForWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirhop.db")
	db1, err := OpenWithTimeout(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := OpenWithTimeout(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	site := &model.Site{Name: "example", OriginalURL: "https://example.com/", CanonicalURL: "https://example.com/", Hostname: "example.com"}
	if err = db1.CreateSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	tx, err := db1.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET updated_at=? WHERE id=?`, unix(time.Now()), site.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	var started atomic.Bool
	go func() {
		started.Store(true)
		done <- db2.SetSiteCWD(ctx, site.ID, "/other")
	}()
	for !started.Load() {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second handle did not wait for writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second handle stayed blocked after writer committed")
	}
	site, err = db1.SiteByID(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if site.CWD != "/other" {
		t.Fatalf("cwd=%q", site.CWD)
	}
}

func TestEntryUpsertRetriesAfterBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirhop.db")
	db1, err := OpenWithTimeout(path, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	db2, err := OpenWithTimeout(path, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	site := &model.Site{Name: "example", OriginalURL: "https://example.com/", CanonicalURL: "https://example.com/", Hostname: "example.com"}
	if err = db1.CreateSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	root, err := db1.EntryByPath(ctx, site.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db1.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET updated_at=? WHERE id=?`, unix(time.Now()), site.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		size := int64(1)
		_, err := db2.UpsertEntriesNoRecount(ctx, []model.Entry{{SiteID: site.ID, ParentID: &root.ID, Name: "file.zip", NormalizedPath: "/file.zip", URL: "https://example.com/file.zip", Type: model.EntryTypeFile, Size: &size}})
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("upsert did not retry SQLITE_BUSY: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upsert stayed blocked after writer committed")
	}
	if _, err = db1.EntryByPath(ctx, site.ID, "/file.zip", false); err != nil {
		t.Fatal(err)
	}
}

// An already-initialized database must reopen without taking the write lock, so
// a second dirhop process can start while another holds a long write
// transaction. This is the regression behind "initialize schema: context
// deadline exceeded": startup used to run the schema (a write) every time.
func TestReopenSkipsSchemaWriteWhenCurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirhop.db")
	db1, err := OpenWithTimeout(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()

	// Hold a write transaction open so the single SQLite writer is occupied.
	tx, err := db1.SQL().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE app_state SET active_site_id=NULL WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}

	// Opening a second handle must succeed quickly: it only reads user_version
	// and finds the schema already current, so it never waits for the writer.
	start := time.Now()
	db2, err := OpenWithTimeout(path, 2*time.Second)
	if err != nil {
		t.Fatalf("reopen of an initialized db blocked on the write lock: %v", err)
	}
	defer db2.Close()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("reopen took %v; it should not have contended for the write lock", elapsed)
	}
}

// A persistent write lock during schema init must surface a clear, actionable
// message instead of a bare "context deadline exceeded". An exclusive lock on
// the file is held so even the schema's write is blocked.
func TestSchemaInitReportsLockClearly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirhop.db")

	// Hold an exclusive lock on the file via a raw connection in rollback-journal
	// mode, so any other writer (including schema init) is blocked outright.
	holder, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(50)&_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	// A victim that must initialize the schema cannot get the lock; after the
	// bounded retry it must return the actionable message.
	victim := &DB{busyTimeout: 20 * time.Millisecond, path: path}
	victim.sql, err = sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(20)")
	if err != nil {
		t.Fatal(err)
	}
	defer victim.sql.Close()
	victim.sql.SetMaxOpenConns(1)

	shortCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	err = victim.ensureSchema(shortCtx)
	if err == nil {
		t.Fatal("ensureSchema succeeded despite an exclusive lock")
	}
	if !strings.Contains(err.Error(), "locked by another process") {
		t.Fatalf("schema-init lock error = %q, want an actionable 'locked by another process' message", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q should name the database path %q", err, path)
	}
}

// PurgeRemovedEntries hard-deletes soft-removed rows and refreshes aggregates;
// Vacuum then reclaims the freed pages. This is the cleanup path for a DB that
// has grown with stale rows across rescans.
func TestPurgeRemovedEntriesAndVacuum(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirhop.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	s := &model.Site{Name: "ex", OriginalURL: "https://ex.test/", CanonicalURL: "https://ex.test/", Hostname: "ex.test"}
	if err := db.CreateSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	root, err := db.EntryByPath(ctx, s.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC()
	for _, name := range []string{"keep.txt", "gone1.txt", "gone2.txt"} {
		sz := int64(1)
		if _, err := db.UpsertEntries(ctx, []model.Entry{{SiteID: s.ID, ParentID: &root.ID, Name: name, NormalizedPath: "/" + name, URL: "https://ex.test/" + name, Type: model.EntryTypeFile, Size: &sz, LastSeenAt: seen}}); err != nil {
			t.Fatal(err)
		}
	}
	// Soft-remove two of the three by reconciling against a later scan start.
	if _, err := db.sql.ExecContext(ctx, `UPDATE entries SET removed=1 WHERE normalized_path IN ('/gone1.txt','/gone2.txt')`); err != nil {
		t.Fatal(err)
	}

	stats, err := db.SiteEntryStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].RemovedRows != 2 || stats[0].LiveEntries != 1 {
		t.Fatalf("stats before purge = %+v, want 1 live / 2 removed", stats)
	}

	deleted, err := db.PurgeRemovedEntries(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("purged %d rows, want 2", deleted)
	}
	// The kept entry survives; the removed ones are gone even including removed.
	if _, err := db.EntryByPath(ctx, s.ID, "/keep.txt", false); err != nil {
		t.Fatalf("purge deleted a live entry: %v", err)
	}
	if _, err := db.EntryByPath(ctx, s.ID, "/gone1.txt", true); err == nil {
		t.Fatal("purge left a soft-removed row behind")
	}
	after, err := db.SiteEntryStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].RemovedRows != 0 || after[0].LiveEntries != 1 {
		t.Fatalf("stats after purge = %+v, want 1 live / 0 removed", after)
	}

	// Checkpoint + vacuum must succeed and leave the DB usable.
	if err := db.CheckpointWAL(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Vacuum(ctx); err != nil {
		t.Fatalf("vacuum: %v", err)
	}
	if _, err := db.EntryByPath(ctx, s.ID, "/keep.txt", false); err != nil {
		t.Fatalf("db unusable after vacuum: %v", err)
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
