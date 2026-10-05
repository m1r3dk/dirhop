package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/m1r3dk/dirhop/internal/app"
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
		if !isHTTPURL(fields[0]) {
			return nil, fmt.Errorf("%w: %s line %d: not an HTTP/HTTPS URL: %s", ErrInvalidArguments, path, n, fields[0])
		}
		spec := urlSpec{URL: fields[0]}
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
		if !isHTTPURL(a) {
			return nil, fmt.Errorf("%w: not an HTTP/HTTPS URL: %s", ErrInvalidArguments, a)
		}
		specs = append(specs, urlSpec{URL: a})
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
	Files   int64  `json:"files"`
	Bytes   int64  `json:"bytes"`
	Error   string `json:"error,omitempty"`
}

// indexURLs indexes each URL in turn (each crawl is itself concurrent). One
// failing site does not stop the rest. With rescan, existing sessions are
// fully re-crawled (scan semantics); otherwise they are reused untouched
// (open semantics). The active session only changes for a single URL.
func indexURLs(ctx context.Context, a *app.App, specs []urlSpec, rescan bool, metadata string, opt *options, out io.Writer) error {
	results := make([]batchResult, 0, len(specs))
	var firstErr error
	for i, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := batchResult{URL: spec.URL}
		site, created, err := a.OpenURL(ctx, spec.URL, spec.Name, len(specs) == 1)
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
			if firstErr == nil {
				firstErr = err
			}
		}
		results = append(results, res)
		if !opt.quiet && !opt.json {
			prefix := ""
			if len(specs) > 1 {
				prefix = fmt.Sprintf("[%d/%d] ", i+1, len(specs))
			}
			switch {
			case err != nil:
				fmt.Fprintf(out, "%sFAILED %s: %v\n", prefix, spec.URL, err)
			case !created && !rescan:
				fmt.Fprintf(out, "%sExisting %s: %d files, %s (use `scan` to re-crawl)\n", prefix, res.Session, res.Files, output.Size(res.Bytes))
			default:
				fmt.Fprintf(out, "%sIndexed %s: %d files, %s\n", prefix, res.Session, res.Files, output.Size(res.Bytes))
			}
		}
	}
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		}
	}
	if opt.json {
		if err := output.JSON(out, results); err != nil {
			return err
		}
	} else if !opt.quiet && len(specs) > 1 {
		fmt.Fprintf(out, "Done: %d indexed, %d failed\n", len(specs)-failed, failed)
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
