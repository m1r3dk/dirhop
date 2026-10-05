package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/config"
	"github.com/m1r3dk/dirhop/internal/crawler"
	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/downloader"
	"github.com/m1r3dk/dirhop/internal/filesystem"
	"github.com/m1r3dk/dirhop/internal/httpclient"
	"github.com/m1r3dk/dirhop/internal/model"
	"github.com/m1r3dk/dirhop/internal/session"
)

// App composes the persistent store, HTTP transport, sessions, virtual
// filesystem, crawler, and downloader used by both CLI interfaces.
type App struct {
	Config   config.Config
	DB       *database.DB
	Sessions *session.Manager
	HTTP     *httpclient.Client
	// Progress, when set, receives live crawl counters.
	Progress func(model.CrawlRun)
	// Logging records whether HTTP request logging is already enabled.
	Logging bool
	// Workers overrides crawl concurrency for the next crawl when > 0.
	Workers int
}

// crawlWorkers resolves concurrency: --workers, then config. Sessions created
// before config support stored a fixed 8; config wins so edits take effect.
func (a *App) crawlWorkers(_ *model.Site) int {
	if a.Workers > 0 {
		return min(a.Workers, 64)
	}
	return max(a.Config.CrawlConcurrency, 1)
}

// bucketWorkers: S3/GCS list endpoints are built for high request rates and
// each page is ~0.5s of pure latency, so default to 4x crawl concurrency.
// Measured on a 77k-object GCS bucket: 8 workers 62s, 16 51s, 32 40s.
func (a *App) bucketWorkers(site *model.Site) int {
	if a.Workers > 0 {
		return min(a.Workers, 64)
	}
	return min(a.crawlWorkers(site)*4, 32)
}

func (a *App) report(run *model.CrawlRun) {
	if a.Progress != nil {
		a.Progress(*run)
	}
}

func Open(cfg config.Config) (*App, error) {
	if err := cfg.Paths.EnsureDirs(); err != nil {
		return nil, err
	}
	db, err := database.OpenWithTimeout(cfg.Paths.Database, cfg.BusyTimeout)
	if err != nil {
		return nil, err
	}
	http := httpclient.New(httpclient.Config{
		Timeout:             cfg.HTTPTimeout,
		Retries:             cfg.Retries,
		MaxIdleConnsPerHost: max(16, cfg.CrawlConcurrency+cfg.DownloadWorkers),
		MaxConnsPerHost:     max(24, cfg.CrawlConcurrency+cfg.DownloadWorkers),
	})
	return &App{Config: cfg, DB: db, Sessions: session.New(db), HTTP: http}, nil
}

func (a *App) Close() error {
	if a.HTTP != nil {
		a.HTTP.CloseIdleConnections()
	}
	if a.DB != nil {
		return a.DB.Close()
	}
	return nil
}

func (a *App) Site(ctx context.Context, selector string) (*model.Site, error) {
	site, err := a.Sessions.Resolve(ctx, selector)
	if errors.Is(err, session.ErrNoActive) {
		return nil, fmt.Errorf("%w: no active session (use -s <name>, `dirhop session use <name>`, or `dirhop <URL>`)", ErrNoSession)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: unknown session %q (see `dirhop sessions`)", ErrNoSession, selector)
	}
	return site, err
}

func (a *App) FS(site *model.Site) *filesystem.FS { return filesystem.New(a.DB, site.ID) }

// OpenURL reuses a canonical URL session or creates and crawls a new one.
func (a *App) OpenURL(ctx context.Context, rawURL, name string, activate bool) (*model.Site, bool, error) {
	site, created, err := a.Sessions.Open(ctx, rawURL, name, activate)
	if err != nil {
		return nil, false, err
	}
	if created {
		if err := a.Crawl(ctx, site, true); err != nil {
			return site, true, err
		}
		site, err = a.DB.SiteByID(ctx, site.ID)
	}
	return site, created, err
}

// Crawl refreshes one site's linked directory hierarchy. Reconciliation only
// removes unseen entries after a fully successful run.
func (a *App) Crawl(ctx context.Context, site *model.Site, full bool) error {
	base, err := url.Parse(site.CanonicalURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	mode := model.CrawlModeIncremental
	if full || site.LastCrawledAt == nil {
		mode = model.CrawlModeFull
	}
	run := &model.CrawlRun{SiteID: site.ID, Mode: mode, Status: model.ScanStatusRunning, StartedAt: time.Now().UTC()}
	if err := a.DB.StartCrawlRun(ctx, run); err != nil {
		return err
	}
	if target, ok := bucket.Detect(site.CanonicalURL); ok {
		err := a.crawlBucket(ctx, site, target, run)
		return a.finishRun(ctx, site, run, err, err != nil, false)
	}
	repo := &crawlRepository{db: a.DB, site: site, base: base, run: run, seenAt: run.StartedAt, conditional: mode == model.CrawlModeIncremental}
	repo.progress = a.report
	workerCount := a.crawlWorkers(site)
	engine, err := crawler.New(a.HTTP, repo, crawler.Config{
		BaseURL: base, Workers: workerCount, MaxDepth: 1024, MaxDirectories: 10_000_000,
		UserAgent: a.Config.UserAgent,
	})
	if err == nil {
		err = engine.Crawl(ctx)
	}

	repo.mu.Lock()
	run.Directories = repo.directories
	run.Files = repo.files
	run.Bytes = repo.bytes
	run.ErrorCount = repo.errors
	rootFailed := repo.rootFailed
	rootUnsupported := repo.rootUnsupported
	rootCause := repo.rootCause
	repo.mu.Unlock()
	if rootFailed && err == nil && rootCause != "" {
		err = fmt.Errorf("%w: root directory could not be indexed: %s", ErrNetwork, rootCause)
		if rootUnsupported {
			err = fmt.Errorf("%w: %s is not a recognizable directory listing or public bucket", ErrUnsupportedListing, site.CanonicalURL)
		}
	}
	return a.finishRun(ctx, site, run, err, rootFailed, rootUnsupported)
}

// finishRun records the outcome, reconciles removals only after a clean
// complete run, recounts aggregates once, and maps failures to exit codes.
func (a *App) finishRun(ctx context.Context, site *model.Site, run *model.CrawlRun, err error, rootFailed, rootUnsupported bool) error {
	switch {
	case errors.Is(err, context.Canceled):
		run.Status = model.ScanStatusCancelled
		run.FailureReason = err.Error()
	case err != nil || rootFailed:
		run.Status = model.ScanStatusFailed
		if err != nil {
			run.FailureReason = err.Error()
		} else {
			run.FailureReason = "root directory could not be indexed"
		}
	default:
		run.Status = model.ScanStatusComplete
	}
	finishErr := a.DB.FinishCrawlRun(context.WithoutCancel(ctx), run)
	if finishErr != nil {
		return finishErr
	}
	if run.Status == model.ScanStatusComplete && run.ErrorCount == 0 {
		if err := a.DB.MarkEntriesRemovedBefore(context.WithoutCancel(ctx), site.ID, run.StartedAt); err != nil {
			return err
		}
	}
	if err := a.DB.Recount(context.WithoutCancel(ctx), site.ID); err != nil {
		return err
	}
	if errors.Is(err, ErrNetwork) || errors.Is(err, ErrUnsupportedListing) || errors.Is(err, context.Canceled) {
		return err
	}
	if rootUnsupported {
		return fmt.Errorf("%w: %s", ErrUnsupportedListing, site.CanonicalURL)
	}
	if run.Status == model.ScanStatusFailed {
		return fmt.Errorf("%w: %s", ErrNetwork, run.FailureReason)
	}
	return err
}

func (a *App) Download(ctx context.Context, site *model.Site, paths []string, destination string, opts downloader.Options) (downloader.Result, error) {
	source := &downloadSource{fs: a.FS(site)}
	d := &downloader.Downloader{Source: source, Client: a.HTTP.HTTPClient(), State: &downloadState{db: a.DB, siteID: site.ID}}
	result, err := d.Download(ctx, paths, destination, opts)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrDownload, err)
	}
	return result, nil
}

type downloadSource struct{ fs *filesystem.FS }

func (s *downloadSource) Expand(ctx context.Context, paths []string) ([]downloader.Entry, error) {
	var out []downloader.Entry
	seen := map[string]struct{}{}
	for _, requested := range paths {
		root, err := s.fs.Resolve(ctx, requested)
		if err != nil {
			return nil, err
		}
		entries, err := s.fs.FilesUnder(ctx, requested)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, ok := seen[entry.NormalizedPath]; ok {
				continue
			}
			seen[entry.NormalizedPath] = struct{}{}
			base := root.NormalizedPath
			if root.IsDir() && base != "/" {
				base = path.Dir(base) // keep "software/" in downloads/software/...
			}
			rel, err := filesystem.RelativeDownloadPath(base, entry)
			if err != nil {
				return nil, err
			}
			size := int64(-1)
			if entry.Size != nil {
				size = *entry.Size
			}
			out = append(out, downloader.Entry{Path: rel, URL: entry.URL, Size: size, ID: entry.ID})
		}
	}
	return out, nil
}

type crawlRepository struct {
	db     *database.DB
	site   *model.Site
	base   *url.URL
	run    *model.CrawlRun
	seenAt time.Time

	mu              sync.Mutex
	directories     int64
	files           int64
	bytes           int64
	errors          int64
	rootFailed      bool
	rootUnsupported bool
	rootCause       string
	conditional     bool
	progress        func(*model.CrawlRun)
}

func (r *crawlRepository) RequestHeaders(ctx context.Context, raw string) http.Header {
	if !r.conditional {
		return nil
	}
	virtual, err := virtualPath(r.base, raw)
	if err != nil {
		return nil
	}
	entry, err := r.db.EntryByPath(ctx, r.site.ID, virtual, false)
	if err != nil {
		return nil
	}
	headers := make(http.Header)
	if entry.ETag != "" {
		headers.Set("If-None-Match", entry.ETag)
	}
	if entry.LastModified != "" {
		headers.Set("If-Modified-Since", entry.LastModified)
	}
	return headers
}

func (r *crawlRepository) CachedDirectories(ctx context.Context, raw string) ([]string, error) {
	virtual, err := virtualPath(r.base, raw)
	if err != nil {
		return nil, err
	}
	parent, err := r.db.EntryByPath(ctx, r.site.ID, virtual, false)
	if err != nil {
		return nil, err
	}
	children, err := r.db.Children(ctx, r.site.ID, parent.ID)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, child := range children {
		if child.IsDir() {
			urls = append(urls, child.URL)
		}
	}
	return urls, nil
}

func (r *crawlRepository) RecordDirectory(ctx context.Context, outcome crawler.DirectoryOutcome) error {
	dirPath, err := virtualPath(r.base, outcome.URL)
	if err != nil {
		return r.recordError(ctx, outcome, err)
	}
	parent, err := r.db.EntryByPath(ctx, r.site.ID, dirPath, false)
	if errors.Is(err, sql.ErrNoRows) {
		return r.recordError(ctx, outcome, fmt.Errorf("directory missing from index: %s", dirPath))
	}
	if err != nil {
		return err
	}

	if outcome.Error != "" {
		if dirPath == "/" {
			r.mu.Lock()
			r.rootFailed = true
			r.rootUnsupported = strings.Contains(outcome.Error, "unsupported directory listing")
			r.rootCause = outcome.Error
			r.mu.Unlock()
		}
		return r.recordError(ctx, outcome, errors.New(outcome.Error))
	}
	if outcome.StatusCode == http.StatusNotModified {
		if err := r.db.TouchDirectFiles(ctx, r.site.ID, parent.ID, r.seenAt); err != nil {
			return err
		}
	}

	if outcome.Parser != "" && r.site.ParserType == "" {
		if err := r.db.SetSiteParser(ctx, r.site.ID, outcome.Parser); err != nil {
			return err
		}
		r.site.ParserType = outcome.Parser
	}
	parent.ETag = outcome.ETag
	parent.LastModified = outcome.LastModified
	parent.LastSeenAt = r.seenAt
	parent.UpdatedAt = time.Now().UTC()
	if _, err := r.db.UpsertEntriesNoRecount(ctx, []model.Entry{*parent}); err != nil {
		return err
	}

	batch := make([]model.Entry, 0, len(outcome.Entries))
	for _, parsed := range outcome.Entries {
		if parsed.URL == nil {
			continue
		}
		normalized, err := virtualPath(r.base, parsed.URL.String())
		if err != nil || normalized == "/" {
			continue
		}
		entryType := model.EntryTypeFile
		if parsed.IsDir {
			entryType = model.EntryTypeDirectory
		}
		entry := model.Entry{
			SiteID: r.site.ID, ParentID: &parent.ID, Name: parsed.Name,
			NormalizedPath: normalized, URL: parsed.URL.String(), Type: entryType,
			Size: parsed.Size, ModifiedAt: parsed.Modified, LastSeenAt: r.seenAt,
		}
		batch = append(batch, entry)
	}
	if _, err := r.db.UpsertEntriesNoRecount(ctx, batch); err != nil {
		return err
	}

	r.mu.Lock()
	r.directories++
	for _, entry := range batch {
		if entry.Type == model.EntryTypeFile {
			r.files++
			if entry.Size != nil {
				r.bytes += *entry.Size
			}
		}
	}
	if r.progress != nil {
		snapshot := *r.run
		snapshot.Directories, snapshot.Files, snapshot.Bytes, snapshot.ErrorCount = r.directories, r.files, r.bytes, r.errors
		r.progress(&snapshot)
	}
	r.mu.Unlock()
	return nil
}

func (r *crawlRepository) recordError(ctx context.Context, outcome crawler.DirectoryOutcome, cause error) error {
	r.mu.Lock()
	r.errors++
	r.mu.Unlock()
	return r.db.AddCrawlError(ctx, &model.CrawlError{
		RunID: r.run.ID, SiteID: r.site.ID, URL: outcome.URL,
		Path: outcome.URL, Operation: "crawl", Message: cause.Error(),
	})
}

func virtualPath(base *url.URL, raw string) (string, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	basePath := path.Clean("/" + base.Path)
	targetPath := path.Clean("/" + target.Path)
	if targetPath == basePath {
		return "/", nil
	}
	prefix := strings.TrimSuffix(basePath, "/") + "/"
	if !strings.HasPrefix(targetPath, prefix) {
		return "", fmt.Errorf("URL outside session root: %s", raw)
	}
	return path.Clean("/" + strings.TrimPrefix(targetPath, prefix)), nil
}
