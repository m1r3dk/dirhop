package session

import (
	"context"
	"errors"
	"testing"

	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/model"
)

func TestCanonicalURL(t *testing.T) {
	got, err := CanonicalURL("HTTPS://Example.COM:443/a/../Files%20Here?C=N&O=D&token=x#frag")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://example.com/Files%20Here/?token=x"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err = CanonicalURL("file:///tmp"); err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestManagerLifecycle(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(db)
	if _, err = m.Active(ctx); !errors.Is(err, ErrNoActive) {
		t.Fatalf("active error=%v", err)
	}
	s, created, err := m.Open(ctx, "https://EXAMPLE.com:443/files?C=N&O=A", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected creation")
	}
	active, err := m.Active(ctx)
	if err != nil || active.ID != s.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	again, created, err := m.Open(ctx, "https://example.com/files/", "different", false)
	if err != nil {
		t.Fatal(err)
	}
	if created || again.ID != s.ID {
		t.Fatalf("session was not reused: %+v %v", again, created)
	}
	renamed, err := m.Rename(ctx, s.Name, "My Session")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "my-session" {
		t.Fatalf("name=%q", renamed.Name)
	}
	if _, err = m.Use(ctx, renamed.Name); err != nil {
		t.Fatal(err)
	}
	if err = m.Remove(ctx, renamed.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Active(ctx); !errors.Is(err, ErrNoActive) {
		t.Fatalf("active after delete=%v", err)
	}
}

// New bucket sessions are named by bucket only (wustl), not the full host.
func TestBucketSessionsNamedByBucket(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(db)
	cases := map[string]string{
		"https://wustl.s3-us-west-2.amazonaws.com/":       "wustl",
		"https://amazetest.storage.googleapis.com/":       "amazetest",
		"https://acct.blob.core.windows.net/documents/":   "documents",
		"https://s3.us-east-2.amazonaws.com/path-bucket/": "path-bucket",
	}
	for raw, want := range cases {
		s, created, err := m.Open(ctx, raw, "", false)
		if err != nil || !created {
			t.Fatalf("open %s: created=%v err=%v", raw, created, err)
		}
		if s.Name != want {
			t.Fatalf("open %s: name=%q want %q", raw, s.Name, want)
		}
	}
	// A non-bucket host still falls back to host-based naming.
	s, _, err := m.Open(ctx, "https://mirror.example.com/pub/", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "mirror-example-com-pub" {
		t.Fatalf("non-bucket name=%q", s.Name)
	}
}

// Two sessions that point at the same S3 bucket through different URL spellings
// (virtual-hosted vs path-style) must be detected as duplicates, keeping the one
// with the richer index.
func TestDuplicatesDetectsSameBucketDifferentURLs(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(db)

	// Same bucket "wustl" in us-east-1, two addressing styles.
	vhost, _, err := m.Open(ctx, "https://wustl.s3.amazonaws.com/", "", false)
	if err != nil {
		t.Fatal(err)
	}
	pathStyle, _, err := m.Open(ctx, "https://s3.amazonaws.com/wustl/", "", false)
	if err != nil {
		t.Fatal(err)
	}
	// A genuinely different bucket must not be grouped with them.
	if _, _, err := m.Open(ctx, "https://other.s3.amazonaws.com/", "", false); err != nil {
		t.Fatal(err)
	}

	// Give the virtual-hosted session more live entries so it is the one kept.
	root, err := db.EntryByPath(ctx, vhost.ID, "/", false)
	if err != nil {
		t.Fatal(err)
	}
	seen := vhost.CreatedAt
	for _, p := range []string{"/a", "/b", "/c"} {
		if _, err := db.UpsertEntries(ctx, []model.Entry{{SiteID: vhost.ID, ParentID: &root.ID, Name: p[1:], NormalizedPath: p, URL: "https://wustl.s3.amazonaws.com" + p, Type: model.EntryTypeFile, LastSeenAt: seen}}); err != nil {
			t.Fatal(err)
		}
	}

	groups, err := m.Duplicates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("found %d duplicate groups, want 1: %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Keep.ID != vhost.ID {
		t.Fatalf("kept session %d, want the richer index %d", g.Keep.ID, vhost.ID)
	}
	if len(g.Duplicates) != 1 || g.Duplicates[0].ID != pathStyle.ID {
		t.Fatalf("duplicates=%+v, want just the path-style session %d", g.Duplicates, pathStyle.ID)
	}
}

// NormalizeNames renames legacy host-based names to bucket names and keeps them
// unique when two different buckets share a bucket label.
func TestNormalizeNames(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := New(db)

	// Simulate legacy sessions by forcing the old host-based names.
	a, _, err := m.Open(ctx, "https://wustl.s3-us-west-2.amazonaws.com/", "wustl-s3-us-west-2-amazonaws-com", false)
	if err != nil {
		t.Fatal(err)
	}
	// A different region, same bucket label, also legacy-named.
	b, _, err := m.Open(ctx, "https://wustl.s3-eu-west-1.amazonaws.com/", "wustl-s3-eu-west-1-amazonaws-com", false)
	if err != nil {
		t.Fatal(err)
	}

	n, err := m.NormalizeNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("renamed %d, want 2", n)
	}
	ra, err := m.db.SiteByID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := m.db.SiteByID(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{ra.Name: true, rb.Name: true}
	if len(names) != 2 || !names["wustl"] || !names["wustl-2"] {
		t.Fatalf("normalized names not unique bucket names: %q, %q", ra.Name, rb.Name)
	}
	// Running again is a no-op.
	n2, err := m.NormalizeNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("second normalize renamed %d, want 0", n2)
	}
}
