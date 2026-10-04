// Package downloader downloads indexed HTTP files while preserving their relative
// hierarchy beneath a caller-provided destination.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"time"
)

// Entry is the minimal indexed-file data required by the downloader.
// Path must be a relative slash-separated output path.
type Entry struct {
	Path string
	URL  string
	// Size is the expected byte size. A negative value means unknown.
	Size int64
	// ID is an opaque caller identifier (e.g. index entry ID) echoed in events.
	ID int64
}

// EntrySource lets filesystem or database packages expand files and recursive
// directory requests without coupling the downloader to their concrete types.
type EntrySource interface {
	Expand(ctx context.Context, paths []string) ([]Entry, error)
}

// StateSink receives lifecycle events suitable for persistent download state.
// Implementations should be safe for repeated events. Calls are serialized.
type StateSink interface {
	Record(ctx context.Context, event Event) error
}

// Status describes a file download lifecycle state.
type Status string

const (
	StatusPlanned   Status = "planned"
	StatusRunning   Status = "running"
	StatusSkipped   Status = "skipped"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// Event is emitted as a file progresses through the download plan.
type Event struct {
	Entry      Entry
	Status     Status
	OutputPath string
	Bytes      int64
	Attempt    int
	Err        error
}

// ExistingPolicy controls behavior when the final output file already exists.
type ExistingPolicy uint8

const (
	// ExistingError reports an error and leaves the existing file untouched.
	ExistingError ExistingPolicy = iota
	// ExistingSkip leaves the existing file untouched and reports it as skipped.
	ExistingSkip
	// ExistingOverwrite replaces the existing file after a successful download.
	ExistingOverwrite
)

// Options controls a download plan. Zero values select conservative defaults.
type Options struct {
	Concurrency int
	Policy      ExistingPolicy
	Retries     int
	Backoff     time.Duration
	MaxBackoff  time.Duration

	// Segments enables optional parallel range downloads for known-size files.
	// Values below 2 disable segmentation. Existing partial files always use the
	// normal resume path instead.
	Segments         int
	SegmentThreshold int64

	// MaxRate is a plan-wide approximate byte-per-second ceiling. Zero disables it.
	MaxRate int64

	// Restart discards a partial file instead of resuming it.
	Restart bool
	// Include and Exclude are slash-path glob filters applied after expansion.
	Include string
	Exclude string
}

// Result summarizes one explicit or recursively expanded plan.
type Result struct {
	Planned   int
	Completed int
	Skipped   int
	Failed    int
	Bytes     int64
}

// PlanError contains independent per-file failures. Successful files are not
// rolled back.
type PlanError struct {
	Failures map[string]error
}

func (e *PlanError) Error() string {
	return fmt.Sprintf("download plan failed for %d file(s)", len(e.Failures))
}

func (e *PlanError) Unwrap() error {
	for _, err := range e.Failures {
		return err
	}
	return nil
}

// Downloader expands indexed paths and executes bounded HTTP downloads.
type Downloader struct {
	Source EntrySource
	State  StateSink
	Client *http.Client
}

// Download expands paths through Source and downloads the resulting plan.
func (d *Downloader) Download(ctx context.Context, paths []string, destination string, opts Options) (Result, error) {
	if d.Source == nil {
		return Result{}, errors.New("downloader: nil entry source")
	}
	entries, err := d.Source.Expand(ctx, paths)
	if err != nil {
		return Result{}, fmt.Errorf("expand download paths: %w", err)
	}
	entries = filterEntries(entries, opts.Include, opts.Exclude)
	return d.DownloadPlan(ctx, entries, destination, opts)
}

func filterEntries(entries []Entry, include, exclude string) []Entry {
	if include == "" && exclude == "" {
		return entries
	}
	out := entries[:0]
	for _, entry := range entries {
		if include != "" {
			matched, _ := path.Match(include, entry.Path)
			nameMatched, _ := path.Match(include, path.Base(entry.Path))
			if !matched && !nameMatched {
				continue
			}
		}
		if exclude != "" {
			matched, _ := path.Match(exclude, entry.Path)
			nameMatched, _ := path.Match(exclude, path.Base(entry.Path))
			if matched || nameMatched {
				continue
			}
		}
		out = append(out, entry)
	}
	return out
}
