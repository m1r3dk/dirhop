package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/model"
	"github.com/m1r3dk/dirhop/internal/output"
	"github.com/m1r3dk/dirhop/internal/session"
)

// urlSpec is one batch line: a URL and an optional session name.
type urlSpec struct {
	URL  string
	Name string
}

// readURLFile parses one URL per line. Blank lines and # comments are
// ignored; an optional second field sets the session name. "-" reads stdin.
// Lines are validated up front so a typo fails before any network work.
func readURLFile(path string, stdin io.Reader) ([]urlSpec, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
		}
		defer f.Close()
		r = f
	}
	var specs []urlSpec
	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 2 {
			return nil, fmt.Errorf("%w: %s line %d: expected \"URL [name]\"", ErrInvalidArguments, path, n)
		}
		raw := normalizeURL(fields[0])
		if !isHTTPURL(raw) {
			return nil, fmt.Errorf("%w: %s line %d: not an HTTP/HTTPS URL: %s", ErrInvalidArguments, path, n, fields[0])
		}
		spec := urlSpec{URL: raw}
		if len(fields) == 2 {
			spec.Name = fields[1]
		}
		specs = append(specs, spec)
	}
	return specs, scanner.Err()
}

// collectURLs merges positional URLs and -f file entries, dropping duplicate
// canonical URLs so a site is never crawled twice in one run.
func collectURLs(args []string, file, name string, stdin io.Reader) ([]urlSpec, error) {
	var specs []urlSpec
	for _, a := range args {
		raw := normalizeURL(a)
		if !isHTTPURL(raw) {
			return nil, fmt.Errorf("%w: not an HTTP/HTTPS URL: %s", ErrInvalidArguments, a)
		}
		specs = append(specs, urlSpec{URL: raw})
	}
	if file != "" {
		fromFile, err := readURLFile(file, stdin)
		if err != nil {
			return nil, err
		}
		specs = append(specs, fromFile...)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("%w: provide at least one URL or -f FILE", ErrInvalidArguments)
	}
	if name != "" {
		if len(specs) > 1 {
			return nil, fmt.Errorf("%w: --name applies to a single URL; put names in the file as \"URL name\"", ErrInvalidArguments)
		}
		specs[0].Name = name
	}
	seen := map[string]bool{}
	out := specs[:0]
	for _, s := range specs {
		canonical, err := session.CanonicalURL(s.URL)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrInvalidArguments, s.URL, err)
		}
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, s)
		}
	}
	return out, nil
}

type batchResult struct {
	URL     string `json:"url"`
	Session string `json:"session,omitempty"`
	Created bool   `json:"created"`
	Skipped bool   `json:"skipped,omitempty"`
	Files   int64  `json:"files"`
	Bytes   int64  `json:"bytes"`
	Error   string `json:"error,omitempty"`
}

// indexURLs indexes each URL (each crawl is itself concurrent). One failing
// site does not stop the rest. With rescan, existing sessions are fully
// re-crawled (scan semantics); otherwise they are reused untouched (open
// semantics). The active session only changes for a single URL.
//
// opt.parallel sites are indexed at once. This is the difference between
// indexing a 30k-bucket list in minutes versus days: most of a single bucket's
// wall-clock time is network latency (and dead hosts time out), so overlapping
// many buckets keeps the pipeline full. SQLite writes stay safe because the
// database serializes write transactions internally.
func indexURLs(ctx context.Context, a *app.App, specs []urlSpec, rescan bool, metadata string, opt *options, out io.Writer) error {
	// inaccessible accumulates targets that cannot be scanned (preflight drops +
	// scan-phase failures) so they can be written to a retry file at the end.
	var inaccessible []failedTarget
	if opt.preflight && len(specs) > 0 {
		pr, err := preflightFilter(ctx, a, specs, opt, out)
		if err != nil {
			return err
		}
		specs = pr.kept
		inaccessible = append(inaccessible, pr.inaccessible...)
		if len(specs) == 0 {
			if !opt.quiet && !opt.json {
				fmt.Fprintln(out, "Preflight: no accessible targets to scan.")
			}
			if werr := writeFailedFile(opt.failedFile, inaccessible, out, opt); werr != nil {
				return werr
			}
			return nil
		}
	}
	results := make([]batchResult, len(specs))
	errs := make([]error, len(specs))
	single := len(specs) == 1
	if !single {
		// Per-crawl progress uses carriage returns. During a batch, several crawls
		// can update it while result lines are printed, which makes output look like
		// "Directories... [202/33820] Indexed..." on one line. Batch scans already
		// have [done/total] result lines, so keep that as the only progress signal.
		savedProgress := a.Progress
		a.Progress = nil
		defer func() { a.Progress = savedProgress }()
	}

	// indexOne runs the full open/crawl/enrich pipeline for one spec and returns
	// its result plus the typed error (kept so exit codes stay meaningful). It
	// never changes the active session during a batch.
	indexOne := func(spec urlSpec) (batchResult, error) {
		res := batchResult{URL: spec.URL}
		// A bucket that already completed successfully is not re-crawled unless the
		// caller forces it with --rescan. Existing sessions that failed, were
		// cancelled, or never finished are retried so a big list can be run again
		// to pick up only the ones that still need work.
		existing, lookupErr := a.DB.SiteByCanonicalURL(ctx, canonicalOrRaw(spec.URL))
		if lookupErr == nil && existing.ScanStatus == model.ScanStatusComplete && !opt.forceRescan {
			res.Session, res.Files, res.Bytes = existing.Name, existing.FileCount, existing.TotalSize
			res.Skipped = true
			if single {
				_ = a.DB.SetActiveSite(ctx, existing.ID)
			}
			return res, nil
		}
		site, created, err := a.OpenURL(ctx, spec.URL, spec.Name, single)
		if err == nil && !created && rescan {
			err = a.Crawl(ctx, site, true)
		}
		if err == nil {
			err = enrich(ctx, a, site, metadata, opt, out)
		}
		if site != nil {
			if fresh, e := a.DB.SiteByID(ctx, site.ID); e == nil {
				site = fresh
			}
			res.Session, res.Files, res.Bytes = site.Name, site.FileCount, site.TotalSize
		}
		res.Created = created
		if err != nil {
			res.Error = err.Error()
		}
		return res, err
	}

	workers := resolveWorkers(opt.parallel, len(specs))

	var printMu sync.Mutex
	var done atomic.Int64
	// Live "currently crawling" footer. Batch output prints one result line per
	// finished bucket; this adds a single redrawn line under them showing which
	// buckets are in flight right now, so a long-running big bucket does not look
	// like a stall. Terminal-only, so pipes/files/JSON stay clean.
	showFooter := !opt.quiet && !opt.json && len(specs) > 1 && isTerminalWriter(out)
	inflight := map[int]string{}
	footerShown := false
	clearFooter := func() { // caller holds printMu
		if footerShown {
			fmt.Fprint(out, "\r\x1b[K")
			footerShown = false
		}
	}
	drawFooter := func() { // caller holds printMu
		if !showFooter || len(inflight) == 0 {
			return
		}
		labels := make([]string, 0, len(inflight))
		for _, l := range inflight {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		const maxShown = 4
		shown := labels
		extra := 0
		if len(shown) > maxShown {
			extra = len(shown) - maxShown
			shown = shown[:maxShown]
		}
		line := "crawling " + strings.Join(shown, ", ")
		if extra > 0 {
			line += fmt.Sprintf(" (+%d more)", extra)
		}
		fmt.Fprintf(out, "\r\x1b[K%s", line)
		footerShown = true
	}
	startInflight := func(i int, label string) {
		if !showFooter {
			return
		}
		printMu.Lock()
		defer printMu.Unlock()
		inflight[i] = label
		clearFooter()
		drawFooter()
	}
	endInflight := func(i int) {
		if !showFooter {
			return
		}
		printMu.Lock()
		defer printMu.Unlock()
		delete(inflight, i)
		clearFooter()
		drawFooter()
	}
	report := func(i int, spec urlSpec, res batchResult) {
		if opt.quiet || opt.json {
			return
		}
		printMu.Lock()
		defer printMu.Unlock()
		clearFooter()
		prefix := ""
		if len(specs) > 1 {
			// With concurrency, finish order is not input order, so count
			// completions rather than labeling with the input index.
			prefix = fmt.Sprintf("[%d/%d] ", done.Add(1), len(specs))
		}
		switch {
		case res.Error != "":
			fmt.Fprintf(out, "%sFAILED %s: %s\n", prefix, spec.URL, res.Error)
		case res.Skipped:
			fmt.Fprintf(out, "%sSkipped %s: already scanned, %d files, %s (use --rescan to re-crawl)\n", prefix, res.Session, res.Files, output.Size(res.Bytes))
		case !res.Created && !rescan:
			fmt.Fprintf(out, "%sExisting %s: %d files, %s (use `scan` to re-crawl)\n", prefix, res.Session, res.Files, output.Size(res.Bytes))
		default:
			fmt.Fprintf(out, "%sIndexed %s: %d files, %s\n", prefix, res.Session, res.Files, output.Size(res.Bytes))
		}
		drawFooter()
	}

	if workers == 1 {
		for i, spec := range specs {
			if err := ctx.Err(); err != nil {
				return err
			}
			startInflight(i, targetLabel(spec.URL))
			results[i], errs[i] = indexOne(spec)
			endInflight(i)
			report(i, spec, results[i])
		}
	} else {
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					if err := ctx.Err(); err != nil {
						results[i] = batchResult{URL: specs[i].URL, Error: err.Error()}
						errs[i] = err
						continue
					}
					startInflight(i, targetLabel(specs[i].URL))
					results[i], errs[i] = indexOne(specs[i])
					endInflight(i)
					report(i, specs[i], results[i])
				}
			}()
		}
		for i := range specs {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}
	if showFooter {
		printMu.Lock()
		clearFooter()
		printMu.Unlock()
	}

	var firstErr error
	failed := 0
	skipped := 0
	for i, r := range results {
		switch {
		case r.Error != "":
			failed++
			inaccessible = append(inaccessible, failedTarget{r.URL, r.Error})
			if firstErr == nil {
				firstErr = errs[i]
			}
		case r.Skipped:
			skipped++
		}
	}
	if opt.json {
		if err := output.JSON(out, results); err != nil {
			return err
		}
	} else if !opt.quiet && len(specs) > 1 {
		fmt.Fprintf(out, "Done: %d indexed, %d skipped, %d failed\n", len(specs)-failed-skipped, skipped, failed)
	}
	if werr := writeFailedFile(opt.failedFile, inaccessible, out, opt); werr != nil {
		return werr
	}
	if firstErr == nil {
		return nil
	}
	// Failures were already printed per URL; return a short summary that keeps
	// the first failure's category so the exit code stays meaningful.
	if opt.json || opt.quiet {
		return fmt.Errorf("%d of %d URLs failed: %w", failed, len(specs), firstErr)
	}
	return quietError{fmt.Errorf("%d of %d URLs failed: %w", failed, len(specs), firstErr)}
}

// quietError carries an exit code without being printed again by main.
type quietError struct{ error }

func (q quietError) Unwrap() error { return q.error }

// canonicalOrRaw returns the canonical form of a URL for a DB lookup, falling
// back to the raw URL when canonicalization fails (the later open will surface
// the real error).
func canonicalOrRaw(raw string) string {
	if c, err := session.CanonicalURL(raw); err == nil {
		return c
	}
	return raw
}

// targetLabel is a short, human-friendly name for a target used in the live
// "crawling ..." footer: the bucket/container name for a recognized bucket,
// otherwise the hostname.
func targetLabel(rawURL string) string {
	if t, ok := bucket.Detect(rawURL); ok && t.Bucket != "" {
		return t.Bucket
	}
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return rawURL
}

// writeFailedFile records inaccessible/failed targets to a file so a large run
// can be retried against just the failures. Each line is "<url>  # <reason>",
// which `scan -f` reads back (the trailing comment is ignored on re-read). An
// empty path means the caller did not request a file; nothing is written. When
// there are no failures and a path was given, any stale file is removed so the
// file always reflects the latest run.
func writeFailedFile(path string, failed []failedTarget, out io.Writer, opt *options) error {
	if path == "" {
		return nil
	}
	if len(failed) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
		}
		return nil
	}
	var b strings.Builder
	for _, f := range failed {
		fmt.Fprintf(&b, "%s  # %s\n", f.URL, f.Reason)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	if !opt.quiet && !opt.json {
		fmt.Fprintf(out, "Wrote %d failed target(s) to %s\n", len(failed), path)
	}
	return nil
}

// resolveWorkers turns the --parallel value into a worker count. 0 means auto:
// scale with the workload up to a sane cap, since indexing is network-bound and
// most buckets spend their time waiting. An explicit value is honored (clamped
// to a hard ceiling); either way the pool never exceeds the number of sites.
func resolveWorkers(parallel, sites int) int {
	const (
		autoCap = 64  // auto never exceeds this
		hardCap = 256 // explicit --parallel ceiling
	)
	if sites < 1 {
		return 1
	}
	switch {
	case parallel <= 0: // auto
		w := sites
		if w > autoCap {
			w = autoCap
		}
		return w
	case parallel > hardCap:
		return min(hardCap, sites)
	default:
		return min(parallel, sites)
	}
}

// preflightResult is the outcome of the preflight phase: the specs worth
// scanning (kept) and the ones dropped as inaccessible with a short reason, so
// callers can record the latter to a file.
type preflightResult struct {
	kept         []urlSpec
	inaccessible []failedTarget
}

// failedTarget is a target that could not be scanned, with a short reason.
type failedTarget struct {
	URL    string
	Reason string
}

// preflightFilter checks each target's accessibility with one cheap request and
// returns the specs worth fully scanning plus the inaccessible ones. Targets
// that are private, missing (dead host / no such bucket), or erroring are
// dropped, so a large list is not spent crawling targets that cannot be read.
// The check runs concurrently and prints live progress for big lists.
func preflightFilter(ctx context.Context, a *app.App, specs []urlSpec, opt *options, out io.Writer) (preflightResult, error) {
	// Buckets that already completed are kept without a network request: indexOne
	// skips them cheaply unless --rescan is set. This keeps re-running a large
	// list fast and avoids re-probing everything already indexed.
	var toCheck []urlSpec
	var alreadyDone []urlSpec
	for _, s := range specs {
		if !opt.forceRescan {
			if site, err := a.DB.SiteByCanonicalURL(ctx, canonicalOrRaw(s.URL)); err == nil && site.ScanStatus == model.ScanStatusComplete {
				alreadyDone = append(alreadyDone, s)
				continue
			}
		}
		toCheck = append(toCheck, s)
	}
	verdicts := make([]app.Preflight, len(toCheck))
	workers := resolveWorkers(opt.parallel, len(toCheck))
	showProgress := !opt.quiet && !opt.json
	if showProgress {
		fmt.Fprintf(out, "Preflight: checking %d targets with %d workers (%d already scanned)...\n", len(toCheck), workers, len(alreadyDone))
	}
	jobs := make(chan int)
	var checked atomic.Int64
	// Live progress: a single carriage-return line updated a few times a second
	// so a 30k-target preflight never looks frozen. Off for quiet/json.
	stopProgress := make(chan struct{})
	var progressDone sync.WaitGroup
	if showProgress && len(toCheck) > 1 {
		progressDone.Add(1)
		go func() {
			defer progressDone.Done()
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopProgress:
					return
				case <-ticker.C:
					fmt.Fprintf(out, "\r\x1b[KPreflight: %d/%d checked", checked.Load(), len(toCheck))
				}
			}
		}()
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					verdicts[i] = app.Preflight{URL: toCheck[i].URL, Err: err}
					checked.Add(1)
					continue
				}
				verdicts[i] = a.PreflightAccess(ctx, toCheck[i].URL)
				checked.Add(1)
			}
		}()
	}
	for i := range toCheck {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if showProgress && len(toCheck) > 1 {
		close(stopProgress)
		progressDone.Wait()
		fmt.Fprintf(out, "\r\x1b[KPreflight: %d/%d checked\n", checked.Load(), len(toCheck))
	}
	if err := ctx.Err(); err != nil {
		return preflightResult{}, err
	}

	kept := make([]urlSpec, 0, len(specs))
	kept = append(kept, alreadyDone...)
	var inaccessible []failedTarget
	var public, denied, missing, errored int
	for i, v := range verdicts {
		switch {
		case v.Accessible():
			public++
			kept = append(kept, toCheck[i])
		case v.Access == bucket.AccessDenied:
			denied++
			inaccessible = append(inaccessible, failedTarget{toCheck[i].URL, "private (access denied)"})
		case v.Access == bucket.AccessMissing:
			missing++
			inaccessible = append(inaccessible, failedTarget{toCheck[i].URL, "missing (no such bucket/host)"})
		default:
			errored++
			reason := "errored"
			if v.Err != nil {
				reason = "errored: " + v.Err.Error()
			}
			inaccessible = append(inaccessible, failedTarget{toCheck[i].URL, reason})
		}
	}
	if !opt.quiet && !opt.json {
		fmt.Fprintf(out, "Preflight summary: %d accessible, %d private, %d missing, %d errored",
			public, denied, missing, errored)
		fmt.Fprintf(out, " -> scanning %d/%d\n", len(kept), len(specs))
	}
	return preflightResult{kept: kept, inaccessible: inaccessible}, nil
}
