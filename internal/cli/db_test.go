package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// The `db` command group must inspect and reclaim space end to end: after a
// rescan soft-removes an entry, `db status` reports it, `db prune --apply`
// hard-deletes it, and `db vacuum` shrinks the file without corrupting the DB.
func TestDBPruneAndVacuumReclaimRemovedRows(t *testing.T) {
	var changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pub/" {
			http.NotFound(w, r)
			return
		}
		if changed.Load() {
			// Second scan: a.zip is gone, so reconciliation soft-removes it.
			fmt.Fprint(w, `<title>Index of /pub</title><a href="b.zip">b.zip</a>`)
			return
		}
		fmt.Fprint(w, `<title>Index of /pub</title><a href="a.zip">a.zip</a>`)
	}))
	defer server.Close()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	_ = os.WriteFile(configPath, []byte("database = \""+filepath.Join(tmp, "db")+"\"\nretries = 0\n"), 0o600)

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		root, cleanup, err := newRoot(&out, &out, configPath)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		root.SetArgs(args)
		err = root.Execute()
		return out.String(), err
	}

	if _, err := run("scan", server.URL+"/pub/", "--no-preflight", "--name", "pub"); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	// Rescan with a.zip removed; a clean complete run soft-removes it.
	changed.Store(true)
	if _, err := run("scan", server.URL+"/pub/", "--no-preflight", "--rescan", "--name", "pub"); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	// db status must show the soft-removed row.
	status, err := run("db", "status")
	if err != nil {
		t.Fatalf("db status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "reclaimable") {
		t.Fatalf("db status did not report reclaimable rows:\n%s", status)
	}

	// Dry-run prune changes nothing.
	dry, err := run("db", "prune")
	if err != nil {
		t.Fatalf("db prune dry-run: %v\n%s", err, dry)
	}
	if !strings.Contains(dry, "Would delete") {
		t.Fatalf("prune dry-run output unexpected:\n%s", dry)
	}

	// Apply prune then vacuum.
	applied, err := run("db", "prune", "--apply")
	if err != nil {
		t.Fatalf("db prune --apply: %v\n%s", err, applied)
	}
	if !strings.Contains(applied, "Deleted") {
		t.Fatalf("prune --apply output unexpected:\n%s", applied)
	}
	vac, err := run("db", "vacuum")
	if err != nil {
		t.Fatalf("db vacuum: %v\n%s", err, vac)
	}
	if !strings.Contains(vac, "Done.") {
		t.Fatalf("vacuum did not finish:\n%s", vac)
	}

	// After prune the session is still usable and now shows no reclaimable rows.
	status2, err := run("db", "status")
	if err != nil {
		t.Fatalf("db status after prune: %v\n%s", err, status2)
	}
	if strings.Contains(status2, "reclaimable") {
		t.Fatalf("reclaimable rows remain after prune:\n%s", status2)
	}
	if ls, err := run("-s", "pub", "ls", "/"); err != nil || !strings.Contains(ls, "b.zip") {
		t.Fatalf("session unusable after cleanup: %v\n%s", err, ls)
	}
}
