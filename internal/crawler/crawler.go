// Package crawler coordinates bounded recursive directory crawling.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/1jehuang/dirclone/internal/parser"
)

const defaultMaxBodyBytes int64 = 8 << 20

// HTTPDoer is satisfied by http.Client and httpclient.Client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Repository records the complete result of each attempted directory fetch.
// Implementations may persist successful entries and failures transactionally.
type Repository interface {
	RecordDirectory(context.Context, DirectoryOutcome) error
}

// CacheProvider is an optional repository capability used by incremental
// refreshes. It supplies validators and cached child directories for 304s.
type CacheProvider interface {
	RequestHeaders(context.Context, string) http.Header
	CachedDirectories(context.Context, string) ([]string, error)
}

// Config bounds crawl concurrency, depth, count, response size, and headers.
type Config struct {
	BaseURL        *url.URL
	Workers        int
	MaxDepth       int
	MaxDirectories int
	MaxBodyBytes   int64
	UserAgent      string
	RequestHeaders http.Header
}

// DirectoryOutcome describes one attempted directory and is the only value the
// crawler writes through Repository.
type DirectoryOutcome struct {
	URL          string
	Depth        int
	StatusCode   int
	Parser       string
	Entries      []parser.Entry
	ETag         string
	LastModified string
	Error        string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// Crawler recursively fetches directory pages without fetching file bodies.
type Crawler struct {
	client HTTPDoer
	repo   Repository
	config Config
	base   *url.URL
}

type crawlTask struct {
	url   *url.URL
	depth int
}

type crawlResult struct {
	outcome   DirectoryOutcome
	directory []*url.URL
}

// New validates configuration and constructs a crawler.
func New(client HTTPDoer, repository Repository, config Config) (*Crawler, error) {
	if client == nil {
		return nil, errors.New("crawler: nil HTTP client")
	}
	if repository == nil {
		return nil, errors.New("crawler: nil repository")
	}
	if config.BaseURL == nil {
		return nil, errors.New("crawler: nil base URL")
	}
	base, err := parser.ResolveURL(config.BaseURL, config.BaseURL, config.BaseURL.String(), true)
	if err != nil {
		return nil, fmt.Errorf("crawler: invalid base URL: %w", err)
	}
	if config.Workers <= 0 {
		config.Workers = 4
	}
	if config.MaxDepth < 0 {
		return nil, errors.New("crawler: MaxDepth must be non-negative")
	}
	if config.MaxDirectories <= 0 {
		config.MaxDirectories = 10_000
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.UserAgent == "" {
		config.UserAgent = "dirclone/1"
	}
	config.BaseURL = base
	if config.RequestHeaders == nil {
		config.RequestHeaders = make(http.Header)
	} else {
		config.RequestHeaders = config.RequestHeaders.Clone()
	}
	return &Crawler{client: client, repo: repository, config: config, base: base}, nil
}

// Crawl visits the configured base directory and safe linked directories. A
// directory failure is recorded and does not cancel unrelated work.
func (crawler *Crawler) Crawl(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	crawlCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tasks := make(chan crawlTask)
	results := make(chan crawlResult, crawler.config.Workers)
	var workers sync.WaitGroup
	for i := 0; i < crawler.config.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range tasks {
				result := crawler.fetch(crawlCtx, task)
				select {
				case results <- result:
				case <-crawlCtx.Done():
					return
				}
			}
		}()
	}
	defer func() {
		close(tasks)
		workers.Wait()
	}()

	pending := []crawlTask{{url: cloneURL(crawler.base), depth: 0}}
	visited := map[string]struct{}{crawler.base.String(): {}}
	scheduled := 1
	inFlight := 0

	for len(pending) > 0 || inFlight > 0 {
		var send chan crawlTask
		var next crawlTask
		if len(pending) > 0 && inFlight < crawler.config.Workers {
			send = tasks
			next = pending[0]
		}

		select {
		case <-crawlCtx.Done():
			return crawlCtx.Err()
		case send <- next:
			pending = pending[1:]
			inFlight++
		case result := <-results:
			inFlight--
			if err := crawler.repo.RecordDirectory(crawlCtx, result.outcome); err != nil {
				cancel()
				return fmt.Errorf("crawler: record directory %s: %w", result.outcome.URL, err)
			}
			if result.outcome.Error != "" || result.outcome.Depth >= crawler.config.MaxDepth {
				continue
			}
			for _, directoryURL := range result.directory {
				if scheduled >= crawler.config.MaxDirectories {
					break
				}
				key := directoryURL.String()
				if _, exists := visited[key]; exists {
					continue
				}
				visited[key] = struct{}{}
				scheduled++
				pending = append(pending, crawlTask{url: directoryURL, depth: result.outcome.Depth + 1})
			}
		}
	}
	return nil
}

func (crawler *Crawler) fetch(ctx context.Context, task crawlTask) crawlResult {
	outcome := DirectoryOutcome{URL: task.url.String(), Depth: task.depth, StartedAt: time.Now()}
	finish := func(result crawlResult) crawlResult {
		result.outcome.FinishedAt = time.Now()
		return result
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, task.url.String(), nil)
	if err != nil {
		outcome.Error = err.Error()
		return finish(crawlResult{outcome: outcome})
	}
	for key, values := range crawler.config.RequestHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if cache, ok := crawler.repo.(CacheProvider); ok {
		for key, values := range cache.RequestHeaders(ctx, task.url.String()) {
			for _, value := range values {
				request.Header.Set(key, value)
			}
		}
	}
	if request.Header.Get("User-Agent") == "" {
		request.Header.Set("User-Agent", crawler.config.UserAgent)
	}

	response, err := crawler.client.Do(request)
	if err != nil {
		outcome.Error = err.Error()
		return finish(crawlResult{outcome: outcome})
	}
	if response.Body == nil {
		outcome.Error = "crawler: response has no body"
		return finish(crawlResult{outcome: outcome})
	}
	defer response.Body.Close()

	outcome.StatusCode = response.StatusCode
	outcome.ETag = response.Header.Get("ETag")
	outcome.LastModified = response.Header.Get("Last-Modified")
	actualURL := task.url
	if response.Request != nil && response.Request.URL != nil {
		actualURL = response.Request.URL
	}
	canonicalActual, err := parser.ResolveURL(crawler.base, crawler.base, actualURL.String(), true)
	if err != nil {
		outcome.Error = "crawler: redirect escaped allowed root"
		return finish(crawlResult{outcome: outcome})
	}
	outcome.URL = canonicalActual.String()

	if response.StatusCode == http.StatusNotModified {
		var directories []*url.URL
		if cache, ok := crawler.repo.(CacheProvider); ok {
			cached, cacheErr := cache.CachedDirectories(ctx, outcome.URL)
			if cacheErr != nil {
				outcome.Error = cacheErr.Error()
				return finish(crawlResult{outcome: outcome})
			}
			for _, raw := range cached {
				if directory, resolveErr := parser.ResolveURL(crawler.base, canonicalActual, raw, true); resolveErr == nil {
					directories = append(directories, directory)
				}
			}
		}
		return finish(crawlResult{outcome: outcome, directory: directories})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		outcome.Error = fmt.Sprintf("crawler: directory returned HTTP %d", response.StatusCode)
		return finish(crawlResult{outcome: outcome})
	}

	limited := io.LimitReader(response.Body, crawler.config.MaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		outcome.Error = fmt.Sprintf("crawler: read directory: %v", err)
		return finish(crawlResult{outcome: outcome})
	}
	if int64(len(body)) > crawler.config.MaxBodyBytes {
		outcome.Error = fmt.Sprintf("crawler: directory body exceeds %d bytes", crawler.config.MaxBodyBytes)
		return finish(crawlResult{outcome: outcome})
	}

	parserName, parsedEntries, err := parser.Parse(strings.NewReader(string(body)), response.Header, canonicalActual)
	outcome.Parser = parserName
	if errors.Is(err, parser.ErrUnsupportedListing) && task.depth > 0 {
		// Linked subdirectory with no anchors is an empty listing, not an error.
		err, parsedEntries = nil, nil
	}
	if err != nil {
		outcome.Error = err.Error()
		return finish(crawlResult{outcome: outcome})
	}

	entries := make([]parser.Entry, 0, len(parsedEntries))
	directories := make([]*url.URL, 0)
	for _, entry := range parsedEntries {
		if entry.URL == nil {
			continue
		}
		safeURL, err := parser.ResolveURL(crawler.base, canonicalActual, entry.URL.String(), entry.IsDir)
		if err != nil {
			continue
		}
		entry.URL = safeURL
		entries = append(entries, entry)
		if entry.IsDir {
			directories = append(directories, cloneURL(safeURL))
		}
	}
	outcome.Entries = entries
	return finish(crawlResult{outcome: outcome, directory: directories})
}

func cloneURL(source *url.URL) *url.URL {
	clone := *source
	return &clone
}
