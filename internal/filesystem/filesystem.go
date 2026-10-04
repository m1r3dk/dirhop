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

	"github.com/1jehuang/dirclone/internal/database"
	"github.com/1jehuang/dirclone/internal/model"
)

var (
	ErrNotFound     = errors.New("indexed path not found")
	ErrNotDirectory = errors.New("indexed path is not a directory")
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

// Walk invokes fn for the root and every descendant in path order.
func (f *FS) Walk(ctx context.Context, root string, fn func(model.Entry) error) error {
	if fn == nil {
		return errors.New("walk callback is nil")
	}
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return err
	}
	entries, err := f.db.EntriesUnder(ctx, f.siteID, e.NormalizedPath, false)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = fn(entry); err != nil {
			return err
		}
	}
	return nil
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
	if !e.IsDir() {
		return []model.Entry{}, nil
	}
	all, err := f.db.EntriesUnder(ctx, f.siteID, e.NormalizedPath, false)
	if err != nil {
		return nil, err
	}
	out := make([]model.Entry, 0)
	for _, x := range all {
		if x.IsFile() {
			out = append(out, x)
		}
	}
	return out, nil
}

// Find filters entries beneath root. Glob matches either the base name or path
// relative to root. Regex is compiled once and applied to the normalized path.
func (f *FS) Find(ctx context.Context, root string, opt model.FindOptions) ([]model.Entry, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	all, err := f.db.EntriesUnder(ctx, f.siteID, e.NormalizedPath, opt.IncludeRemoved)
	if err != nil {
		return nil, err
	}
	var re *regexp.Regexp
	if opt.Regex != "" {
		re, err = regexp.Compile(opt.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
	}
	exts := map[string]struct{}{}
	for _, x := range opt.Extensions {
		x = strings.ToLower(x)
		if x != "" && !strings.HasPrefix(x, ".") {
			x = "." + x
		}
		exts[x] = struct{}{}
	}
	out := make([]model.Entry, 0)
	for _, x := range all {
		if x.NormalizedPath == e.NormalizedPath && e.IsDir() {
			continue
		}
		if opt.Type != "" && x.Type != opt.Type {
			continue
		}
		if len(exts) > 0 {
			if _, ok := exts[strings.ToLower(x.Extension)]; !ok {
				continue
			}
		}
		if opt.MinSize != nil && (x.Size == nil || *x.Size < *opt.MinSize) {
			continue
		}
		if opt.MaxSize != nil && (x.Size == nil || *x.Size > *opt.MaxSize) {
			continue
		}
		if opt.ModifiedAfter != nil && (x.ModifiedAt == nil || x.ModifiedAt.Before(*opt.ModifiedAfter)) {
			continue
		}
		if opt.ModifiedBefore != nil && (x.ModifiedAt == nil || x.ModifiedAt.After(*opt.ModifiedBefore)) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(x.NormalizedPath, e.NormalizedPath), "/")
		if opt.Glob != "" {
			a, _ := path.Match(opt.Glob, x.Name)
			b, _ := path.Match(opt.Glob, rel)
			if !a && !b {
				continue
			}
		}
		if re != nil && !re.MatchString(x.NormalizedPath) {
			continue
		}
		out = append(out, x)
		if opt.Limit > 0 && len(out) >= opt.Limit {
			break
		}
	}
	return out, nil
}

// Search performs a case-insensitive literal search over names and paths.
func (f *FS) Search(ctx context.Context, root, query string, limit int) ([]model.Entry, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return []model.Entry{}, nil
	}
	all, err := f.Find(ctx, root, model.FindOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]model.Entry, 0)
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Name), query) || strings.Contains(strings.ToLower(e.NormalizedPath), query) {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *FS) DiskUsage(ctx context.Context, root string) (model.DiskUsage, error) {
	e, err := f.Resolve(ctx, root)
	if err != nil {
		return model.DiskUsage{}, err
	}
	all, err := f.db.EntriesUnder(ctx, f.siteID, e.NormalizedPath, false)
	if err != nil {
		return model.DiskUsage{}, err
	}
	var d model.DiskUsage
	for _, x := range all {
		switch x.Type {
		case model.EntryTypeFile:
			d.Files++
			if x.Size != nil {
				d.Bytes += *x.Size
			}
		case model.EntryTypeDirectory:
			d.Directories++
		}
	}
	return d, nil
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
	all, err := f.db.EntriesUnder(ctx, f.siteID, e.NormalizedPath, false)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, x := range all {
		if x.IsFile() || (includeDirectories && x.IsDir()) {
			out = append(out, x.URL)
		}
	}
	return out, nil
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
