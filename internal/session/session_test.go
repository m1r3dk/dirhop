package session

import (
	"context"
	"errors"
	"testing"

	"github.com/m1r3dk/dirclone/internal/database"
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
