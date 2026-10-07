package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/m1r3dk/dirhop/internal/model"
)

// Progress must render inside the interactive shell. The shell writes command
// output through an *x/term.Terminal, so a printer that only recognized
// *os.File left long in-shell crawls (refresh/scan) looking frozen.
func TestProgressPrinterDrawsForShellTerminal(t *testing.T) {
	var buf bytes.Buffer
	terminal := term.NewTerminal(&buf, "")

	update, done := progressPrinter(terminal, false)
	if update == nil {
		t.Fatal("progress printer is a no-op for the shell terminal; in-shell crawls show no progress")
	}
	update(model.CrawlRun{Directories: 3, Files: 7, Bytes: 2048})
	done()
	if buf.Len() == 0 {
		t.Fatal("progress printer produced no output for the shell terminal")
	}
}

func TestProgressPrinterShowsCurrentTargetAndPath(t *testing.T) {
	var buf bytes.Buffer
	terminal := term.NewTerminal(&buf, "")

	update, done := progressPrinter(terminal, false)
	update(model.CrawlRun{TargetName: "bucket-name", CurrentPath: "/dir/file.bin", Directories: 3, Files: 7, Bytes: 2048})
	done()

	out := buf.String()
	if !strings.Contains(out, "Crawling: bucket-name:/dir/file.bin") {
		t.Fatalf("progress did not include current target/path: %q", out)
	}
}

// Quiet mode and non-terminal writers (pipes, files, JSON output) must stay
// silent so scripted and machine-readable runs are not corrupted.
func TestProgressPrinterSilentWhenNotTerminal(t *testing.T) {
	if update, _ := progressPrinter(&bytes.Buffer{}, false); update != nil {
		t.Error("progress printer should be silent for a plain buffer")
	}
	terminal := term.NewTerminal(&bytes.Buffer{}, "")
	if update, _ := progressPrinter(terminal, true); update != nil {
		t.Error("progress printer should be silent when quiet is set")
	}
}

func TestBatchProgressShowsRowsWithCurrentPaths(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	progress := newBatchProgress(&buf, &mu)

	mu.Lock()
	progress.startLocked(0, "alpha")
	progress.startLocked(1, "beta")
	mu.Unlock()
	progress.update(model.CrawlRun{SiteID: 10, TargetName: "alpha", CurrentPath: "/orders/a.zip", Directories: 4, Files: 100, Bytes: 2048})
	progress.lastDraw = time.Now().Add(-time.Second)
	progress.update(model.CrawlRun{SiteID: 11, TargetName: "beta", CurrentPath: "/logs/2024/", Directories: 2, Files: 7, Bytes: 512})

	out := buf.String()
	for _, want := range []string{"alpha", "/orders/a.zip", "dirs=4", "files=100", "beta", "/logs/2024/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("batch progress missing %q in:\n%q", want, out)
		}
	}
	if strings.Contains(out, "(+") {
		t.Fatalf("batch progress should render per-target rows, not a summarized footer:\n%q", out)
	}
}
