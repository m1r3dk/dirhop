package cli

import (
	"bytes"
	"testing"

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
