package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/m1r3dk/dirclone/internal/model"
	"github.com/m1r3dk/dirclone/internal/output"
)

// progressPrinter renders live crawl counters on a TTY at most ~8 times a
// second, and stays silent when stderr is not a terminal (pipes, scripts).
func progressPrinter(w io.Writer, quiet bool) (func(model.CrawlRun), func()) {
	f, ok := w.(*os.File)
	if quiet || !ok || !term.IsTerminal(int(f.Fd())) {
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
