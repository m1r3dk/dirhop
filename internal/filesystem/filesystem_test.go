package filesystem

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/1jehuang/dirclone/internal/database"
	"github.com/1jehuang/dirclone/internal/model"
)

func fixture(t *testing.T) (*database.DB, *FS) {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s := &model.Site{Name: "test", OriginalURL: "https://example.test/", CanonicalURL: "https://example.test/", Hostname: "example.test"}
	if err = db.CreateSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	root, err := db.EntryByPath(ctx, s.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dirs, err := db.UpsertEntries(ctx, []model.Entry{{SiteID: s.ID, ParentID: &root.ID, Name: "docs", NormalizedPath: "/docs", URL: "https://example.test/docs/", Type: model.EntryTypeDirectory, LastSeenAt: now}, {SiteID: s.ID, ParentID: &root.ID, Name: "images", NormalizedPath: "/images", URL: "https://example.test/images/", Type: model.EntryTypeDirectory, LastSeenAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	size10, size20 := int64(10), int64(20)
	_, err = db.UpsertEntries(ctx, []model.Entry{{SiteID: s.ID, ParentID: &dirs[0].ID, Name: "Read Me.txt", NormalizedPath: "/docs/Read Me.txt", URL: "https://example.test/docs/Read%20Me.txt", Type: model.EntryTypeFile, Size: &size10, ModifiedAt: &now, LastSeenAt: now}, {SiteID: s.ID, ParentID: &dirs[0].ID, Name: "manual.pdf", NormalizedPath: "/docs/manual.pdf", URL: "https://example.test/docs/manual.pdf", Type: model.EntryTypeFile, Size: &size20, ModifiedAt: &now, LastSeenAt: now}, {SiteID: s.ID, ParentID: &dirs[1].ID, Name: "logo.png", NormalizedPath: "/images/logo.png", URL: "https://example.test/images/logo.png", Type: model.EntryTypeFile, Size: &size20, ModifiedAt: &now, LastSeenAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	return db, New(db, s.ID)
}

func TestResolveListChdirWalkAndFiles(t *testing.T) {
	db, fs := fixture(t)
	defer db.Close()
	ctx := context.Background()
	list, err := fs.List(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "docs" {
		t.Fatalf("list=%+v", list)
	}
	if err = fs.Chdir(ctx, "docs"); err != nil {
		t.Fatal(err)
	}
	cwd, err := fs.CWD(ctx)
	if err != nil || cwd != "/docs" {
		t.Fatalf("cwd=%q err=%v", cwd, err)
	}
	e, err := fs.Resolve(ctx, "manual.pdf")
	if err != nil || e.NormalizedPath != "/docs/manual.pdf" {
		t.Fatalf("entry=%+v err=%v", e, err)
	}
	if _, err = fs.List(ctx, "manual.pdf"); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("list file err=%v", err)
	}
	var walked []string
	if err = fs.Walk(ctx, "/docs", func(e model.Entry) error { walked = append(walked, e.NormalizedPath); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"/docs", "/docs/manual.pdf", "/docs/Read Me.txt"}
	if !reflect.DeepEqual(walked, want) {
		t.Fatalf("walk=%v", walked)
	}
	files, err := fs.FilesUnder(ctx, "/docs")
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
}

func TestFindSearchDUURLsAndCompletion(t *testing.T) {
	db, fs := fixture(t)
	defer db.Close()
	ctx := context.Background()
	min := int64(15)
	found, err := fs.Find(ctx, "/", model.FindOptions{Glob: "*.pdf", MinSize: &min, Type: model.EntryTypeFile})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "manual.pdf" {
		t.Fatalf("found=%+v", found)
	}
	found, err = fs.Find(ctx, "/", model.FindOptions{Regex: `(?i)logo\.png$`, Extensions: []string{"png"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("regex found=%+v", found)
	}
	results, err := fs.Search(ctx, "/", "read", 0)
	if err != nil || len(results) != 1 {
		t.Fatalf("search=%+v err=%v", results, err)
	}
	du, err := fs.DU(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if du.Files != 3 || du.Directories != 3 || du.Bytes != 50 {
		t.Fatalf("du=%+v", du)
	}
	urls, err := fs.URLs(ctx, "/docs", false)
	if err != nil || len(urls) != 2 {
		t.Fatalf("urls=%v err=%v", urls, err)
	}
	complete, err := fs.Complete(ctx, "/docs/m")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(complete, []string{"/docs/manual.pdf"}) {
		t.Fatalf("complete=%v", complete)
	}
	if p, err := RelativeDownloadPath("/docs", found[0]); err == nil || p != "" {
		t.Fatalf("outside root accepted: %q %v", p, err)
	}
	entry, _ := fs.Resolve(ctx, "/docs/manual.pdf")
	p, err := RelativeDownloadPath("/docs", *entry)
	if err != nil || p != "manual.pdf" {
		t.Fatalf("relative=%q err=%v", p, err)
	}
}
