package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/m1r3dk/dirhop/internal/model"
	"github.com/m1r3dk/dirhop/internal/output"
)

// progressPrinter renders live crawl counters at most ~8 times a second. It
// draws when the target is a real terminal: an *os.File TTY for one-shot CLI
// runs, or the interactive shell's *term.Terminal. It stays silent for pipes,
// scripts, files, and --quiet/--json.
func progressPrinter(w io.Writer, quiet bool) (func(model.CrawlRun), func()) {
	if quiet || !isTerminalWriter(w) {
		return nil, func() {}
	}
	var mu sync.Mutex
	var last time.Time
	var printed bool
	start := time.Now()
	update := func(run model.CrawlRun) {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(last) < 125*time.Millisecond {
			return
		}
		last, printed = time.Now(), true
		rate := float64(run.Directories) / max(time.Since(start).Seconds(), 0.001)
		fmt.Fprintf(w, "\r\x1b[KDirectories: %d  Files: %d  Size: %s  Errors: %d  %.0f dirs/s",
			run.Directories, run.Files, output.Size(run.Bytes), run.ErrorCount, rate)
	}
	done := func() {
		mu.Lock()
		defer mu.Unlock()
		if printed {
			fmt.Fprint(w, "\r\x1b[K")
		}
	}
	return update, done
}

// isTerminalWriter reports whether w draws to an interactive terminal: either a
// TTY-backed *os.File (one-shot CLI) or the shell's *term.Terminal. The shell
// routes all command output through a *term.Terminal, so matching only *os.File
// would silence crawl progress for every in-shell refresh/scan.
func isTerminalWriter(w io.Writer) bool {
	if _, ok := w.(*term.Terminal); ok {
		return true
	}
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}
