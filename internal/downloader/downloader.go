package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const copyBufferSize = 32 * 1024

var (
	errRangeUnsupported = errors.New("server does not support validated byte ranges")
	errResumeRejected   = errors.New("server returned an invalid resume response")
	errSizeMismatch     = errors.New("downloaded size does not match indexed size")
)

type normalizedOptions struct {
	Options
	client *http.Client
}

type fileOutcome struct {
	path   string
	status Status
	bytes  int64
	err    error
}

// DownloadPlan downloads an already-expanded set of indexed file entries.
func (d *Downloader) DownloadPlan(ctx context.Context, entries []Entry, destination string, opts Options) (Result, error) {
	nopts, err := normalizeOptions(d.Client, opts)
	if err != nil {
		return Result{}, err
	}
	if destination == "" {
		return Result{}, errors.New("downloader: empty destination")
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return Result{}, fmt.Errorf("resolve destination: %w", err)
	}
	if err := os.MkdirAll(absDestination, 0o755); err != nil {
		return Result{}, fmt.Errorf("create destination: %w", err)
	}

	prepared := make([]preparedEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		item, prepErr := prepareEntry(absDestination, entry)
		if prepErr != nil {
			return Result{}, prepErr
		}
		if _, exists := seen[item.output]; exists {
			return Result{}, fmt.Errorf("downloader: duplicate output path %q", entry.Path)
		}
		seen[item.output] = struct{}{}
		prepared = append(prepared, item)
	}

	state := &serializedState{sink: d.State}
	for _, item := range prepared {
		if err := state.record(ctx, Event{Entry: item.entry, Status: StatusPlanned, OutputPath: item.output}); err != nil {
			return Result{}, fmt.Errorf("record planned state for %q: %w", item.entry.Path, err)
		}
	}

	result := Result{Planned: len(prepared)}
	if len(prepared) == 0 {
		return result, nil
	}

	requestSlots := make(chan struct{}, nopts.Concurrency)
	limiter := newRateLimiter(nopts.MaxRate)
	jobs := make(chan preparedEntry)
	outcomes := make(chan fileOutcome, len(prepared))
	var workers sync.WaitGroup
	workerCount := min(nopts.Concurrency, len(prepared))
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				outcomes <- d.downloadOne(ctx, item, nopts, state, requestSlots, limiter)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, item := range prepared {
			jobs <- item
		}
	}()
	go func() {
		workers.Wait()
		close(outcomes)
	}()

	failures := make(map[string]error)
	for outcome := range outcomes {
		result.Bytes += outcome.bytes
		switch outcome.status {
		case StatusCompleted:
			result.Completed++
		case StatusSkipped:
			result.Skipped++
		case StatusFailed:
			result.Failed++
			failures[outcome.path] = outcome.err
		}
	}
	if len(failures) > 0 {
		return result, &PlanError{Failures: failures}
	}
	return result, nil
}

type preparedEntry struct {
	entry   Entry
	output  string
	partial string
}

func prepareEntry(destination string, entry Entry) (preparedEntry, error) {
	if entry.Path == "" {
		return preparedEntry{}, errors.New("downloader: empty entry path")
	}
	if entry.Size < -1 {
		return preparedEntry{}, fmt.Errorf("downloader: invalid size for %q", entry.Path)
	}
	parsed, err := url.Parse(entry.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return preparedEntry{}, fmt.Errorf("downloader: invalid HTTP URL for %q", entry.Path)
	}
	path := filepath.FromSlash(entry.Path)
	if filepath.IsAbs(path) {
		return preparedEntry{}, fmt.Errorf("downloader: absolute output path %q", entry.Path)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return preparedEntry{}, fmt.Errorf("downloader: unsafe output path %q", entry.Path)
	}
	output := filepath.Join(destination, clean)
	rel, err := filepath.Rel(destination, output)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return preparedEntry{}, fmt.Errorf("downloader: output path escapes destination %q", entry.Path)
	}
	return preparedEntry{entry: entry, output: output, partial: output + ".part"}, nil
}

func normalizeOptions(client *http.Client, opts Options) (normalizedOptions, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.Retries < 0 || opts.MaxRate < 0 || opts.Segments < 0 || opts.SegmentThreshold < 0 {
		return normalizedOptions{}, errors.New("downloader: options cannot be negative")
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 100 * time.Millisecond
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 2 * time.Second
	}
	if opts.MaxBackoff < opts.Backoff {
		opts.MaxBackoff = opts.Backoff
	}
	if opts.SegmentThreshold == 0 {
		opts.SegmentThreshold = 8 << 20
	}
	if client == nil {
		client = http.DefaultClient
	}
	return normalizedOptions{Options: opts, client: client}, nil
}

func (d *Downloader) downloadOne(ctx context.Context, item preparedEntry, opts normalizedOptions, state *serializedState, requestSlots chan struct{}, limiter *rateLimiter) fileOutcome {
	if err := os.MkdirAll(filepath.Dir(item.output), 0o755); err != nil {
		return d.failed(ctx, item, state, 0, 0, fmt.Errorf("create output directory: %w", err))
	}
	if info, err := os.Stat(item.output); err == nil {
		switch opts.Policy {
		case ExistingSkip:
			if err := state.record(ctx, Event{Entry: item.entry, Status: StatusSkipped, OutputPath: item.output, Bytes: info.Size()}); err != nil {
				return d.failed(ctx, item, state, 0, 0, fmt.Errorf("record skipped state: %w", err))
			}
			return fileOutcome{path: item.entry.Path, status: StatusSkipped}
		case ExistingOverwrite:
		default:
			return d.failed(ctx, item, state, 0, 0, fmt.Errorf("output file already exists: %s", item.output))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return d.failed(ctx, item, state, 0, 0, fmt.Errorf("stat output: %w", err))
	}

	if err := state.record(ctx, Event{Entry: item.entry, Status: StatusRunning, OutputPath: item.output}); err != nil {
		return d.failed(ctx, item, state, 0, 0, fmt.Errorf("record running state: %w", err))
	}
	if opts.Restart {
		if err := os.Remove(item.partial); err != nil && !errors.Is(err, os.ErrNotExist) {
			return d.failed(ctx, item, state, 0, 0, fmt.Errorf("remove partial: %w", err))
		}
	}

	var written int64
	var err error
	partialInfo, statErr := os.Stat(item.partial)
	fresh := errors.Is(statErr, os.ErrNotExist) || (statErr == nil && partialInfo.Size() == 0)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return d.failed(ctx, item, state, 0, 0, fmt.Errorf("stat partial: %w", statErr))
	}
	if opts.Segments >= 2 && item.entry.Size >= opts.SegmentThreshold && item.entry.Size > 0 && fresh {
		written, err = d.downloadSegmented(ctx, item, opts, requestSlots, limiter)
		if errors.Is(err, errRangeUnsupported) {
			_ = os.Remove(item.partial)
			written, err = d.downloadStream(ctx, item, opts, requestSlots, limiter)
		}
	} else {
		written, err = d.downloadStream(ctx, item, opts, requestSlots, limiter)
	}
	if err != nil {
		return d.failed(ctx, item, state, 0, written, err)
	}
	if err := verifySize(item.partial, item.entry.Size); err != nil {
		return d.failed(ctx, item, state, 0, written, err)
	}
	if opts.Policy == ExistingOverwrite {
		if err := replaceFile(item.partial, item.output); err != nil {
			return d.failed(ctx, item, state, 0, written, err)
		}
	} else if err := os.Rename(item.partial, item.output); err != nil {
		return d.failed(ctx, item, state, 0, written, fmt.Errorf("finalize download: %w", err))
	}
	info, err := os.Stat(item.output)
	if err != nil {
		return d.failed(ctx, item, state, 0, written, fmt.Errorf("stat completed output: %w", err))
	}
	if err := state.record(ctx, Event{Entry: item.entry, Status: StatusCompleted, OutputPath: item.output, Bytes: info.Size()}); err != nil {
		return d.failed(ctx, item, state, 0, written, fmt.Errorf("record completed state: %w", err))
	}
	return fileOutcome{path: item.entry.Path, status: StatusCompleted, bytes: info.Size()}
}

func (d *Downloader) failed(ctx context.Context, item preparedEntry, state *serializedState, attempt int, bytes int64, err error) fileOutcome {
	if recordErr := state.record(context.WithoutCancel(ctx), Event{Entry: item.entry, Status: StatusFailed, OutputPath: item.output, Bytes: bytes, Attempt: attempt, Err: err}); recordErr != nil {
		err = errors.Join(err, fmt.Errorf("record failed state: %w", recordErr))
	}
	return fileOutcome{path: item.entry.Path, status: StatusFailed, bytes: bytes, err: err}
}

func (d *Downloader) downloadStream(ctx context.Context, item preparedEntry, opts normalizedOptions, slots chan struct{}, limiter *rateLimiter) (int64, error) {
	var lastErr error
	resumeRestarted := false
	for attempt := 0; attempt <= opts.Retries; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, retryDelay(opts, attempt)); err != nil {
				return 0, err
			}
		}
		written, retry, err := streamAttempt(ctx, opts.client, item, slots, limiter)
		if err == nil {
			return written, nil
		}
		if errors.Is(err, errResumeRejected) && !resumeRestarted {
			if removeErr := os.Remove(item.partial); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return 0, fmt.Errorf("discard rejected partial: %w", removeErr)
			}
			resumeRestarted = true
			attempt--
			continue
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return 0, lastErr
}

func streamAttempt(ctx context.Context, client *http.Client, item preparedEntry, slots chan struct{}, limiter *rateLimiter) (int64, bool, error) {
	partialSize := int64(0)
	if info, err := os.Stat(item.partial); err == nil {
		partialSize = info.Size()
		if item.entry.Size >= 0 && partialSize > item.entry.Size {
			if err := os.Remove(item.partial); err != nil {
				return 0, false, fmt.Errorf("remove oversized partial: %w", err)
			}
			partialSize = 0
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, false, fmt.Errorf("stat partial: %w", err)
	}
	if item.entry.Size >= 0 && partialSize == item.entry.Size {
		return partialSize, false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.entry.URL, nil)
	if err != nil {
		return 0, false, fmt.Errorf("create request: %w", err)
	}
	if partialSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", partialSize))
	}
	resp, err := doBounded(ctx, client, req, slots)
	if err != nil {
		return 0, true, fmt.Errorf("request file: %w", err)
	}
	defer resp.Body.Close()

	appendMode := false
	switch {
	case partialSize > 0 && resp.StatusCode == http.StatusPartialContent:
		start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != partialSize || end < start || (item.entry.Size >= 0 && total != item.entry.Size) {
			return 0, false, fmt.Errorf("%w: invalid Content-Range", errResumeRejected)
		}
		appendMode = true
	case partialSize > 0 && resp.StatusCode == http.StatusOK:
		partialSize = 0
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if resp.StatusCode == http.StatusPartialContent {
			start, _, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
			if !ok || start != 0 || (item.entry.Size >= 0 && total != item.entry.Size) {
				return 0, false, errors.New("invalid Content-Range for download")
			}
		}
	default:
		return 0, retryableStatus(resp.StatusCode), fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(item.partial, flags, 0o644)
	if err != nil {
		return 0, false, fmt.Errorf("open partial: %w", err)
	}
	copied, copyErr := copyWithContext(ctx, file, resp.Body, limiter)
	closeErr := file.Close()
	if copyErr != nil {
		return partialSize + copied, true, fmt.Errorf("write partial: %w", copyErr)
	}
	if closeErr != nil {
		return partialSize + copied, false, fmt.Errorf("close partial: %w", closeErr)
	}
	return partialSize + copied, false, nil
}

func (d *Downloader) downloadSegmented(ctx context.Context, item preparedEntry, opts normalizedOptions, slots chan struct{}, limiter *rateLimiter) (int64, error) {
	if err := probeRanges(ctx, opts.client, item, slots); err != nil {
		return 0, err
	}
	segments := min(opts.Segments, int(item.entry.Size))
	if segments < 2 {
		return 0, errRangeUnsupported
	}
	type segment struct {
		index      int
		start, end int64
		path       string
	}
	pieces := make([]segment, 0, segments)
	base := item.entry.Size / int64(segments)
	extra := item.entry.Size % int64(segments)
	start := int64(0)
	for i := range segments {
		length := base
		if int64(i) < extra {
			length++
		}
		pieces = append(pieces, segment{index: i, start: start, end: start + length - 1, path: fmt.Sprintf("%s.seg-%06d", item.partial, i)})
		start += length
	}
	defer func() {
		for _, piece := range pieces {
			_ = os.Remove(piece.path)
		}
	}()

	segmentCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, len(pieces))
	for _, piece := range pieces {
		piece := piece
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := downloadRangeWithRetry(segmentCtx, opts, item, piece.path, piece.start, piece.end, slots, limiter); err != nil {
				errs <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errs)
	var unsupported bool
	var firstErr error
	for err := range errs {
		if errors.Is(err, errRangeUnsupported) {
			unsupported = true
		} else if firstErr == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
	}
	if unsupported {
		return 0, errRangeUnsupported
	}
	if firstErr != nil {
		return 0, firstErr
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	output, err := os.OpenFile(item.partial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open merged partial: %w", err)
	}
	var merged int64
	for _, piece := range pieces {
		input, openErr := os.Open(piece.path)
		if openErr != nil {
			_ = output.Close()
			return merged, fmt.Errorf("open segment: %w", openErr)
		}
		n, copyErr := io.Copy(output, input)
		closeInputErr := input.Close()
		merged += n
		if copyErr != nil || closeInputErr != nil {
			_ = output.Close()
			return merged, errors.Join(copyErr, closeInputErr)
		}
	}
	if err := output.Close(); err != nil {
		return merged, fmt.Errorf("close merged partial: %w", err)
	}
	if merged != item.entry.Size {
		return merged, fmt.Errorf("%w: got %d, want %d", errSizeMismatch, merged, item.entry.Size)
	}
	return merged, nil
}

func probeRanges(ctx context.Context, client *http.Client, item preparedEntry, slots chan struct{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.entry.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := doBounded(ctx, client, req, slots)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return errRangeUnsupported
	}
	start, end, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || start != 0 || end != 0 || total != item.entry.Size {
		return errRangeUnsupported
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2))
	return nil
}

func downloadRangeWithRetry(ctx context.Context, opts normalizedOptions, item preparedEntry, path string, start, end int64, slots chan struct{}, limiter *rateLimiter) error {
	var lastErr error
	for attempt := 0; attempt <= opts.Retries; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, retryDelay(opts, attempt)); err != nil {
				return err
			}
		}
		retry, err := downloadRangeAttempt(ctx, opts.client, item, path, start, end, slots, limiter)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return lastErr
}

func downloadRangeAttempt(ctx context.Context, client *http.Client, item preparedEntry, path string, start, end int64, slots chan struct{}, limiter *rateLimiter) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.entry.URL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := doBounded(ctx, client, req, slots)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return false, errRangeUnsupported
	}
	if resp.StatusCode != http.StatusPartialContent {
		return retryableStatus(resp.StatusCode), fmt.Errorf("range returned HTTP %d", resp.StatusCode)
	}
	gotStart, gotEnd, total, ok := parseContentRange(resp.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd != end || total != item.entry.Size {
		return false, errRangeUnsupported
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false, err
	}
	copied, copyErr := copyWithContext(ctx, file, io.LimitReader(resp.Body, end-start+2), limiter)
	closeErr := file.Close()
	if copyErr != nil {
		return true, copyErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if copied != end-start+1 {
		return true, fmt.Errorf("range size mismatch: got %d, want %d", copied, end-start+1)
	}
	return false, nil
}

func doBounded(ctx context.Context, client *http.Client, req *http.Request, slots chan struct{}) (*http.Response, error) {
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	resp, err := client.Do(req)
	if err != nil {
		<-slots
		return nil, err
	}
	resp.Body = &slotReadCloser{ReadCloser: resp.Body, release: func() { <-slots }}
	return resp, nil
}

type slotReadCloser struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *slotReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.release)
	return err
}

func parseContentRange(value string) (start, end, total int64, ok bool) {
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	rangeAndTotal := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(rangeAndTotal) != 2 || rangeAndTotal[1] == "*" {
		return 0, 0, 0, false
	}
	bounds := strings.Split(rangeAndTotal[0], "-")
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	start, err1 := strconv.ParseInt(bounds[0], 10, 64)
	end, err2 := strconv.ParseInt(bounds[1], 10, 64)
	total, err3 := strconv.ParseInt(rangeAndTotal[1], 10, 64)
	return start, end, total, err1 == nil && err2 == nil && err3 == nil && start >= 0 && end >= start && total > end
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader, limiter *rateLimiter) (int64, error) {
	buffer := make([]byte, copyBufferSize)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if err := limiter.wait(ctx, n); err != nil {
				return total, err
			}
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func verifySize(path string, expected int64) error {
	if expected < 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != expected {
		return fmt.Errorf("%w: got %d, want %d", errSizeMismatch, info.Size(), expected)
	}
	return nil
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove existing output: %w", err)
	}
	if err := os.Rename(source, destination); err != nil {
		return fmt.Errorf("replace output: %w", err)
	}
	return nil
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func retryDelay(opts normalizedOptions, attempt int) time.Duration {
	delay := opts.Backoff
	for i := 1; i < attempt && delay < opts.MaxBackoff; i++ {
		if delay > opts.MaxBackoff/2 {
			return opts.MaxBackoff
		}
		delay *= 2
	}
	return min(delay, opts.MaxBackoff)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type serializedState struct {
	mu   sync.Mutex
	sink StateSink
}

func (s *serializedState) record(ctx context.Context, event Event) error {
	if s.sink == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sink.Record(ctx, event)
}

type rateLimiter struct {
	mu   sync.Mutex
	rate int64
	next time.Time
}

func newRateLimiter(rate int64) *rateLimiter {
	return &rateLimiter{rate: rate}
}

func (l *rateLimiter) wait(ctx context.Context, bytes int) error {
	if l.rate <= 0 || bytes <= 0 {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	delay := time.Duration(int64(time.Second) * int64(bytes) / l.rate)
	ready := l.next.Add(delay)
	l.next = ready
	l.mu.Unlock()
	return sleepContext(ctx, time.Until(ready))
}
