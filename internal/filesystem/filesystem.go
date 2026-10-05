package filesystem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/model"
)

var (
	ErrNotFound     = errors.New("indexed path not found")
	ErrNotDirectory = errors.New("indexed path is not a directory")
	ErrNotFile      = errors.New("indexed path is not a file")
)

// FS is a site-scoped, SQLite-backed read-only filesystem.
type FS struct {
	db     *database.DB
	siteID int64
}

func New(db *database.DB, siteID int64) *FS { return &FS{db: db, siteID: siteID} }
func (f *FS) SiteID() int64                 { return f.siteID }

// CleanPath resolves a virtual path against the site's persisted CWD.
func (f *FS) CleanPath(ctx context.Context, p string) (string, error) {
	site, err := f.db.SiteByID(ctx, f.siteID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	if !strings.HasPrefix(p, "/") {
		p = path.Join(site.CWD, p)
	}
	p = path.Clean("/" + p)
	return p, nil
}

func (f *FS) Resolve(ctx context.Context, p string) (*model.Entry, error) {
	clean, err := f.CleanPath(ctx, p)
	if err != nil {
		return nil, err
	}
	e, err := f.db.EntryByPath(ctx, f.siteID, clean, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, clean)
	}
	return e, err
}
func (f *FS) Stat(ctx context.Context, p string) (*model.Entry, error) { return f.Resolve(ctx, p) }
func (f *FS) List(ctx context.Context, p string) ([]model.Entry, error) {
	e, err := f.Resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	if !e.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, e.NormalizedPath)
	}
	return f.db.Children(ctx, f.siteID, e.ID)
}

// ChangeDirectory validates a directory and persists it for this site.
func (f *FS) ChangeDirectory(ctx context.Context, p string) (*model.Entry, error) {
	e, err := f.Resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	if !e.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, e.NormalizedPath)
	}
	if err = f.db.SetSiteCWD(ctx, f.siteID, e.NormalizedPath); err != nil {
		return nil, err
	}
	return e, nil
}
func (f *FS) Chdir(ctx context.Context, p string) error {
	_, err := f.ChangeDirectory(ctx, p)
	return err
}
func (f *FS) CWD(ctx context.Context) (string, error) {
	s, err := f.db.SiteByID(ctx, f.siteID)
	if err != nil {
		return "", err
	}
	return s.CWD, nil
}

// Walk invokes fn for the root and every descendant in path order, streaming
// rows from SQLite.
func (f *FS) Walk(ctx context.Context, root string, fn func(model.Entry) error) error {
	if fn == nil {
		return errors.New("walk callback is nil")
	}
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return err
	}
	return f.db.ScanEntries(ctx, f.siteID, e.NormalizedPath, database.EntryFilter{IncludeRoot: true}, fn)
}

// FilesUnder expands a file or directory into downloadable file entries.
func (f *FS) FilesUnder(ctx context.Context, root string) ([]model.Entry, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	if e.IsFile() {
		return []model.Entry{*e}, nil
	}
	out := make([]model.Entry, 0)
	if !e.IsDir() {
		return out, nil
	}
	err = f.db.ScanEntries(ctx, f.siteID, e.NormalizedPath, database.EntryFilter{Type: model.EntryTypeFile}, func(x model.Entry) error {
		out = append(out, x)
		return nil
	})
	return out, err
}

// Find filters entries beneath root. Indexed constraints (type, extension,
// size, date, name glob) run in SQLite; regex and path globs run on the
// already-narrowed stream.
func (f *FS) Find(ctx context.Context, root string, opt model.FindOptions) ([]model.Entry, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if opt.Regex != "" {
		if re, err = regexp.Compile(opt.Regex); err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
	}
	filter := database.EntryFilter{
		IncludeRemoved: opt.IncludeRemoved, Type: opt.Type, Extensions: opt.Extensions,
		MinSize: opt.MinSize, MaxSize: opt.MaxSize,
		ModifiedAfter: opt.ModifiedAfter, ModifiedBefore: opt.ModifiedBefore,
	}
	pathGlob := strings.Contains(opt.Glob, "/")
	if opt.Glob != "" && !pathGlob {
		filter.NameGlob = opt.Glob
	}
	out := make([]model.Entry, 0)
	errLimit := errors.New("limit")
	err = f.db.ScanEntries(ctx, f.siteID, e.NormalizedPath, filter, func(x model.Entry) error {
		if pathGlob {
			rel := strings.TrimPrefix(strings.TrimPrefix(x.NormalizedPath, e.NormalizedPath), "/")
			if ok, _ := path.Match(opt.Glob, rel); !ok {
				return nil
			}
		}
		if re != nil && !re.MatchString(x.NormalizedPath) {
			return nil
		}
		out = append(out, x)
		if opt.Limit > 0 && len(out) >= opt.Limit {
			return errLimit
		}
		return nil
	})
	if errors.Is(err, errLimit) {
		err = nil
	}
	return out, err
}

// Search performs a case-insensitive literal search over names and paths.
func (f *FS) Search(ctx context.Context, root, query string, limit int) ([]model.Entry, error) {
	query = strings.TrimSpace(query)
	out := make([]model.Entry, 0)
	if query == "" {
		return out, nil
	}
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	errLimit := errors.New("limit")
	err = f.db.ScanEntries(ctx, f.siteID, e.NormalizedPath, database.EntryFilter{Substring: query}, func(x model.Entry) error {
		out = append(out, x)
		if limit > 0 && len(out) >= limit {
			return errLimit
		}
		return nil
	})
	if errors.Is(err, errLimit) {
		err = nil
	}
	return out, err
}

// DiskUsage aggregates a subtree inside SQLite without loading entries.
func (f *FS) DiskUsage(ctx context.Context, root string) (model.DiskUsage, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return model.DiskUsage{}, err
	}
	return f.db.DiskUsage(ctx, f.siteID, e.NormalizedPath)
}
func (f *FS) DU(ctx context.Context, root string) (model.DiskUsage, error) {
	return f.DiskUsage(ctx, root)
}

// URLs returns indexed HTTP URLs for a path subtree. Directories are included
// only when includeDirectories is true.
func (f *FS) URLs(ctx context.Context, root string, includeDirectories bool) ([]string, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	filter := database.EntryFilter{}
	if !includeDirectories {
		filter.Type = model.EntryTypeFile
	}
	out := make([]string, 0)
	err = f.db.ScanEntries(ctx, f.siteID, e.NormalizedPath, filter, func(x model.Entry) error {
		if x.IsFile() || x.IsDir() {
			out = append(out, x.URL)
		}
		return nil
	})
	return out, err
}

// Complete returns index-only path completions and never accesses the network.
func (f *FS) Complete(ctx context.Context, input string) ([]string, error) {
	dir, prefix := path.Split(input)
	lookup := dir
	if lookup == "" {
		lookup = "."
	}
	children, err := f.List(ctx, lookup)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, e := range children {
		if strings.HasPrefix(strings.ToLower(e.Name), strings.ToLower(prefix)) {
			v := dir + e.Name
			if e.IsDir() {
				v += "/"
			}
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out, nil
}
func (f *FS) Completions(ctx context.Context, input string) ([]string, error) {
	return f.Complete(ctx, input)
}

// RelativeDownloadPath returns a safe local relative path for an indexed entry.
func RelativeDownloadPath(root string, e model.Entry) (string, error) {
	root = path.Clean("/" + root)
	if e.NormalizedPath != root && !strings.HasPrefix(e.NormalizedPath, strings.TrimSuffix(root, "/")+"/") {
		return "", errors.New("entry is outside requested root")
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(e.NormalizedPath, root), "/")
	if rel == "" {
		rel = e.Name
	}
	local := filepath.FromSlash(rel)
	if local == "." || local == "" || filepath.IsAbs(local) || strings.HasPrefix(local, ".."+string(filepath.Separator)) {
		return "", errors.New("unsafe download path")
	}
	return local, nil
}
