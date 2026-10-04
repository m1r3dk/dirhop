package filesystem

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/m1r3dk/dirclone/internal/database"
	"github.com/m1r3dk/dirclone/internal/model"
)

func BenchmarkFind500k(b *testing.B) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	site := &model.Site{Name: "bench", OriginalURL: "https://example.test/", CanonicalURL: "https://example.test/", Hostname: "example.test"}
	if err := db.CreateSite(ctx, site); err != nil {
		b.Fatal(err)
	}
	root, _ := db.EntryByPath(ctx, site.ID, "/", false)
	now := time.Now()
	entries := make([]model.Entry, 500_000)
	for i := range entries {
		name := fmt.Sprintf("file-%06d.zip", i)
		size := int64(i)
		entries[i] = model.Entry{SiteID: site.ID, ParentID: &root.ID, Name: name, NormalizedPath: "/" + name, URL: "https://example.test/" + name, Type: model.EntryTypeFile, Size: &size, LastSeenAt: now}
	}
	if _, err := db.UpsertEntries(ctx, entries); err != nil {
		b.Fatal(err)
	}
	fs := New(db, site.ID)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Find(ctx, "/", model.FindOptions{Glob: "*.zip", MinSize: ptr(int64(499000))}); err != nil {
			b.Fatal(err)
		}
	}
}

func ptr[T any](v T) *T { return &v }
