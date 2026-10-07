package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/m1r3dk/dirhop/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = sql.ErrNoRows

// DB owns a SQLite connection pool. SQLite itself coordinates processes via WAL;
// within this process, writeMu serializes write transactions so concurrent
// crawls (for example a parallel multi-bucket scan) never contend for the single
// WAL writer and never surface SQLITE_BUSY. Reads are not guarded and run
// concurrently on other pooled connections under WAL.
type DB struct {
	sql         *sql.DB
	busyTimeout time.Duration
	writeMu     sync.Mutex
}

// lockWrite serializes a write transaction. Use as: defer d.lockWrite()().
func (d *DB) lockWrite() func() {
	d.writeMu.Lock()
	return d.writeMu.Unlock
}

func (d *DB) withBusyRetry(ctx context.Context, op func() error) error {
	deadline := time.Now().Add(maxDuration(2*time.Minute, d.busyTimeout))
	delay := 50 * time.Millisecond
	for {
		err := op()
		if err == nil || !isSQLiteBusy(err) || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "database is locked")
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

var memorySequence atomic.Uint64

// Open opens or creates a database and installs the schema.
func Open(path string) (*DB, error) { return OpenWithTimeout(path, 5*time.Second) }

func OpenWithTimeout(path string, busyTimeout time.Duration) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is empty")
	}
	if busyTimeout <= 0 {
		return nil, errors.New("busy timeout must be positive")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := sqliteDSN(path, busyTimeout)
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite has one writer across all processes. Keep one connection per dirhop
	// process so database/sql does not create intra-process lock contention, then
	// rely on WAL + busy_timeout for cross-process writers to take turns.
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	db := &DB{sql: sqldb, busyTimeout: busyTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sqldb.PingContext(ctx); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if _, err := sqldb.ExecContext(ctx, schema); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return db, nil
}

func sqliteDSN(path string, busy time.Duration) string {
	ms := busy.Milliseconds()
	if path == ":memory:" {
		return fmt.Sprintf("file:dirhop-memory-%d?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=synchronous(NORMAL)", memorySequence.Add(1), ms)
	}
	abs, _ := filepath.Abs(path)
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", ms))
	q.Add("_pragma", "synchronous(NORMAL)")
	u.RawQuery = q.Encode()
	return u.String()
}

func (d *DB) Close() error { return d.sql.Close() }
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) CreateSite(ctx context.Context, site *model.Site) error {
	if site == nil {
		return errors.New("site is nil")
	}
	if site.Name == "" || site.CanonicalURL == "" || site.OriginalURL == "" {
		return errors.New("site name and URLs are required")
	}
	if site.CWD == "" {
		site.CWD = "/"
	}
	if site.ScanStatus == "" {
		site.ScanStatus = model.ScanStatusPending
	}
	if site.CrawlConcurrency <= 0 {
		site.CrawlConcurrency = 8
	}
	now := time.Now().UTC()
	if site.CreatedAt.IsZero() {
		site.CreatedAt = now
	}
	if site.UpdatedAt.IsZero() {
		site.UpdatedAt = now
	}
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	site.EntryCount = max(site.EntryCount, 1)
	site.DirectoryCount = max(site.DirectoryCount, 1)
	res, err := tx.ExecContext(ctx, `INSERT INTO sites(name,original_url,canonical_url,hostname,parser_type,cwd,entry_count,file_count,directory_count,total_size,scan_status,crawl_concurrency,created_at,updated_at,last_crawled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, site.Name, site.OriginalURL, site.CanonicalURL, site.Hostname, site.ParserType, site.CWD, site.EntryCount, site.FileCount, site.DirectoryCount, site.TotalSize, site.ScanStatus, site.CrawlConcurrency, unix(site.CreatedAt), unix(site.UpdatedAt), nullableTime(site.LastCrawledAt))
	if err != nil {
		return fmt.Errorf("insert site: %w", err)
	}
	site.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO entries(site_id,parent_id,name,normalized_path,url,type,removed,created_at,updated_at,last_seen_at) VALUES(?,NULL,'','/',?,'directory',0,?,?,?)`, site.ID, site.CanonicalURL, unix(now), unix(now), unix(now))
	if err != nil {
		return fmt.Errorf("insert root entry: %w", err)
	}
	_ = res
	return tx.Commit()
}

const siteColumns = `id,name,original_url,canonical_url,hostname,parser_type,cwd,entry_count,file_count,directory_count,total_size,scan_status,crawl_concurrency,created_at,updated_at,last_crawled_at`

type scanner interface{ Scan(...any) error }

func scanSite(s scanner) (*model.Site, error) {
	var x model.Site
	var created, updated int64
	var last sql.NullInt64
	err := s.Scan(&x.ID, &x.Name, &x.OriginalURL, &x.CanonicalURL, &x.Hostname, &x.ParserType, &x.CWD, &x.EntryCount, &x.FileCount, &x.DirectoryCount, &x.TotalSize, &x.ScanStatus, &x.CrawlConcurrency, &created, &updated, &last)
	if err != nil {
		return nil, err
	}
	x.CreatedAt, x.UpdatedAt = fromUnix(created), fromUnix(updated)
	x.LastCrawledAt = fromNullTime(last)
	return &x, nil
}

func (d *DB) SiteByID(ctx context.Context, id int64) (*model.Site, error) {
	return scanSite(d.sql.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE id=?`, id))
}
func (d *DB) SiteByName(ctx context.Context, name string) (*model.Site, error) {
	return scanSite(d.sql.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE name=? COLLATE NOCASE`, name))
}
func (d *DB) SiteByCanonicalURL(ctx context.Context, u string) (*model.Site, error) {
	return scanSite(d.sql.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE canonical_url=?`, u))
}
func (d *DB) ActiveSite(ctx context.Context) (*model.Site, error) {
	return scanSite(d.sql.QueryRowContext(ctx, `SELECT s.id,s.name,s.original_url,s.canonical_url,s.hostname,s.parser_type,s.cwd,s.entry_count,s.file_count,s.directory_count,s.total_size,s.scan_status,s.crawl_concurrency,s.created_at,s.updated_at,s.last_crawled_at FROM sites s JOIN app_state a ON a.active_site_id=s.id WHERE a.singleton=1`))
}

func (d *DB) ListSites(ctx context.Context) ([]model.Site, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+siteColumns+` FROM sites ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Site
	for rows.Next() {
		x, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}

// SitesByStatus returns sites whose scan_status is in the given set, ordered by
// name. An empty set returns nothing.
func (d *DB) SitesByStatus(ctx context.Context, statuses ...model.ScanStatus) ([]model.Site, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")
	args := make([]any, len(statuses))
	for i, s := range statuses {
		args[i] = string(s)
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE scan_status IN (`+placeholders+`) ORDER BY name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Site
	for rows.Next() {
		x, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *x)
	}
	return out, rows.Err()
}
func (d *DB) SetActiveSite(ctx context.Context, id int64) error {
	defer d.lockWrite()()
	res, err := d.sql.ExecContext(ctx, `UPDATE app_state SET active_site_id=(SELECT id FROM sites WHERE id=?) WHERE singleton=1`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	var got sql.NullInt64
	if err = d.sql.QueryRowContext(ctx, `SELECT active_site_id FROM app_state WHERE singleton=1`).Scan(&got); err != nil {
		return err
	}
	if !got.Valid || got.Int64 != id {
		return sql.ErrNoRows
	}
	return nil
}
func (d *DB) ClearActiveSite(ctx context.Context) error {
	defer d.lockWrite()()
	_, err := d.sql.ExecContext(ctx, `UPDATE app_state SET active_site_id=NULL WHERE singleton=1`)
	return err
}
func (d *DB) SetSiteCWD(ctx context.Context, id int64, cwd string) error {
	defer d.lockWrite()()
	res, err := d.sql.ExecContext(ctx, `UPDATE sites SET cwd=?,updated_at=? WHERE id=?`, cwd, unix(time.Now()), id)
	return affected(res, err)
}
func (d *DB) RenameSite(ctx context.Context, id int64, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("empty site name")
	}
	defer d.lockWrite()()
	res, err := d.sql.ExecContext(ctx, `UPDATE sites SET name=?,updated_at=? WHERE id=?`, name, unix(time.Now()), id)
	return affected(res, err)
}
func (d *DB) DeleteSite(ctx context.Context, id int64) error {
	defer d.lockWrite()()
	res, err := d.sql.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, id)
	return affected(res, err)
}
func (d *DB) SetSiteParser(ctx context.Context, id int64, parserType string) error {
	defer d.lockWrite()()
	res, err := d.sql.ExecContext(ctx, `UPDATE sites SET parser_type=?,updated_at=? WHERE id=?`, parserType, unix(time.Now()), id)
	return affected(res, err)
}
func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const entryColumns = `id,site_id,parent_id,name,normalized_path,url,type,size,modified_at,etag,last_modified,content_type,extension,removed,created_at,updated_at,last_seen_at`

func scanEntry(s scanner) (*model.Entry, error) {
	var x model.Entry
	var parent, size, modified sql.NullInt64
	var removed int
	var created, updated, seen int64
	err := s.Scan(&x.ID, &x.SiteID, &parent, &x.Name, &x.NormalizedPath, &x.URL, &x.Type, &size, &modified, &x.ETag, &x.LastModified, &x.ContentType, &x.Extension, &removed, &created, &updated, &seen)
	if err != nil {
		return nil, err
	}
	if parent.Valid {
		v := parent.Int64
		x.ParentID = &v
	}
	if size.Valid {
		v := size.Int64
		x.Size = &v
	}
	if modified.Valid {
		v := fromUnix(modified.Int64)
		x.ModifiedAt = &v
	}
	x.Removed = removed != 0
	x.CreatedAt = fromUnix(created)
	x.UpdatedAt = fromUnix(updated)
	x.LastSeenAt = fromUnix(seen)
	return &x, nil
}
func (d *DB) EntryByPath(ctx context.Context, siteID int64, path string, includeRemoved bool) (*model.Entry, error) {
	q := `SELECT ` + entryColumns + ` FROM entries WHERE site_id=? AND normalized_path=?`
	if !includeRemoved {
		q += ` AND removed=0`
	}
	return scanEntry(d.sql.QueryRowContext(ctx, q, siteID, path))
}
func (d *DB) EntryByID(ctx context.Context, id int64) (*model.Entry, error) {
	return scanEntry(d.sql.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE id=?`, id))
}
func (d *DB) Children(ctx context.Context, siteID, parentID int64) ([]model.Entry, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE site_id=? AND parent_id=? AND removed=0 ORDER BY type='directory' DESC,name COLLATE NOCASE`, siteID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntryRows(rows)
}

func (d *DB) TouchDirectFiles(ctx context.Context, siteID, parentID int64, seen time.Time) error {
	defer d.lockWrite()()
	_, err := d.sql.ExecContext(ctx, `UPDATE entries SET last_seen_at=?,updated_at=? WHERE site_id=? AND parent_id=? AND type='file' AND removed=0`, unix(seen), unix(time.Now()), siteID, parentID)
	return err
}

// EntriesUnder returns the root and all descendants in path order.
func (d *DB) EntriesUnder(ctx context.Context, siteID int64, root string, includeRemoved bool) ([]model.Entry, error) {
	var out []model.Entry
	err := d.ScanEntries(ctx, siteID, root, EntryFilter{IncludeRoot: true, IncludeRemoved: includeRemoved}, func(e model.Entry) error {
		out = append(out, e)
		return nil
	})
	return out, err
}

// RemovedEntries returns entries that were present in an earlier scan but were
// not seen in the most recent complete scan (soft-deleted). Newest removal
// first. A limit <= 0 means no limit.
func (d *DB) RemovedEntries(ctx context.Context, siteID int64, limit int) ([]model.Entry, error) {
	q := `SELECT ` + entryColumns + ` FROM entries WHERE site_id=? AND removed=1 AND normalized_path<>'/' ORDER BY updated_at DESC, normalized_path COLLATE NOCASE`
	args := []any{siteID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEntryRows(rows)
}

// EntryFilter is evaluated inside SQLite so large indexes are never loaded
// wholesale into memory. Zero values mean no constraint.
type EntryFilter struct {
	IncludeRoot    bool
	IncludeRemoved bool
	OnlyRemoved    bool // when true, return only entries marked removed
	Type           model.EntryType
	NameGlob       string // SQLite GLOB on the base name
	Substring      string // case-insensitive match on name or path
	Extensions     []string
	MinSize        *int64
	MaxSize        *int64
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
}

func subtreeWhere(siteID int64, root string, f EntryFilter) (string, []any) {
	where := `site_id=?`
	args := []any{siteID}
	if root != "/" {
		where += ` AND (normalized_path LIKE ? ESCAPE '^'`
		args = append(args, escapeLike(strings.TrimSuffix(root, "/"))+"/%")
		if f.IncludeRoot {
			where += ` OR normalized_path=?`
			args = append(args, root)
		}
		where += `)`
	} else if !f.IncludeRoot {
		where += ` AND normalized_path<>'/'`
	}
	switch {
	case f.OnlyRemoved:
		where += ` AND removed=1`
	case !f.IncludeRemoved:
		where += ` AND removed=0`
	}
	if f.Type != "" {
		where += ` AND type=?`
		args = append(args, f.Type)
	}
	if f.NameGlob != "" {
		where += ` AND name GLOB ?`
		args = append(args, f.NameGlob)
	}
	if f.Substring != "" {
		where += ` AND (name LIKE ? ESCAPE '^' OR normalized_path LIKE ? ESCAPE '^')`
		like := "%" + escapeLike(f.Substring) + "%"
		args = append(args, like, like)
	}
	if len(f.Extensions) > 0 {
		where += ` AND extension COLLATE NOCASE IN (` + strings.TrimSuffix(strings.Repeat("?,", len(f.Extensions)), ",") + `)`
		for _, ext := range f.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" && !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			args = append(args, ext)
		}
	}
	if f.MinSize != nil {
		where += ` AND size>=?`
		args = append(args, *f.MinSize)
	}
	if f.MaxSize != nil {
		where += ` AND size<=?`
		args = append(args, *f.MaxSize)
	}
	if f.ModifiedAfter != nil {
		where += ` AND modified_at>=?`
		args = append(args, unix(*f.ModifiedAfter))
	}
	if f.ModifiedBefore != nil {
		where += ` AND modified_at<=?`
		args = append(args, unix(*f.ModifiedBefore))
	}
	return where, args
}

// ScanEntries streams matching entries beneath root in path order.
func (d *DB) ScanEntries(ctx context.Context, siteID int64, root string, f EntryFilter, fn func(model.Entry) error) error {
	where, args := subtreeWhere(siteID, root, f)
	rows, err := d.sql.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE `+where+` ORDER BY normalized_path COLLATE NOCASE`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return err
		}
		if err := fn(*e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DiskUsage aggregates a subtree inside SQLite.
func (d *DB) DiskUsage(ctx context.Context, siteID int64, root string) (model.DiskUsage, error) {
	where, args := subtreeWhere(siteID, root, EntryFilter{IncludeRoot: true})
	var du model.DiskUsage
	err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(SUM(type='file'),0), COALESCE(SUM(type='directory'),0), COALESCE(SUM(CASE WHEN type='file' THEN size END),0) FROM entries WHERE `+where, args...).Scan(&du.Files, &du.Directories, &du.Bytes)
	return du, err
}

// IndexStats summarizes the complete local index across all sessions.
func (d *DB) IndexStats(ctx context.Context) (model.IndexStats, error) {
	var stats model.IndexStats
	err := d.sql.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(scan_status='complete'),0),
		COALESCE(SUM(scan_status='failed'),0),
		COALESCE(SUM(scan_status='running'),0),
		COALESCE(SUM(scan_status='pending'),0),
		COALESCE(SUM(scan_status='cancelled'),0),
		COALESCE(SUM(file_count),0),
		COALESCE(SUM(directory_count),0),
		COALESCE(SUM(total_size),0)
		FROM sites`).Scan(&stats.Sites, &stats.CompleteSites, &stats.FailedSites, &stats.RunningSites, &stats.PendingSites, &stats.CancelledSites, &stats.Files, &stats.Directories, &stats.Bytes)
	if err != nil {
		return stats, err
	}
	err = d.sql.QueryRowContext(ctx, `SELECT COALESCE(SUM(type='file'),0), COALESCE(SUM(CASE WHEN type='file' THEN size END),0) FROM entries WHERE removed=1`).Scan(&stats.RemovedFiles, &stats.RemovedBytes)
	if err != nil {
		return stats, err
	}

	typeRows, err := d.sql.QueryContext(ctx, `SELECT type, COUNT(*), COALESCE(SUM(CASE WHEN type='file' THEN size END),0) FROM entries WHERE removed=0 AND normalized_path<>'/' GROUP BY type ORDER BY COUNT(*) DESC, type`)
	if err != nil {
		return stats, err
	}
	defer typeRows.Close()
	for typeRows.Next() {
		var row model.TypeStat
		if err := typeRows.Scan(&row.Type, &row.Count, &row.Bytes); err != nil {
			return stats, err
		}
		stats.Types = append(stats.Types, row)
	}
	if err := typeRows.Err(); err != nil {
		return stats, err
	}

	extRows, err := d.sql.QueryContext(ctx, `SELECT CASE WHEN extension='' THEN '(none)' ELSE extension END AS ext, COUNT(*), COALESCE(SUM(size),0) FROM entries WHERE removed=0 AND type='file' GROUP BY ext ORDER BY COUNT(*) DESC, ext COLLATE NOCASE`)
	if err != nil {
		return stats, err
	}
	defer extRows.Close()
	for extRows.Next() {
		var row model.ExtensionStat
		if err := extRows.Scan(&row.Extension, &row.Count, &row.Bytes); err != nil {
			return stats, err
		}
		stats.Extensions = append(stats.Extensions, row)
	}
	return stats, extRows.Err()
}

func scanEntryRows(rows *sql.Rows) ([]model.Entry, error) {
	var out []model.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
func escapeLike(s string) string {
	r := strings.NewReplacer(`^`, `^^`, `%`, `^%`, `_`, `^_`)
	return r.Replace(s)
}

// UpsertEntries writes a crawler batch atomically and returns entries with IDs populated.
func (d *DB) UpsertEntries(ctx context.Context, entries []model.Entry) ([]model.Entry, error) {
	return d.upsertEntries(ctx, entries, true)
}

// UpsertEntriesNoRecount is used by crawls, which recount once at the end
// instead of rescanning the whole site after every directory.
func (d *DB) UpsertEntriesNoRecount(ctx context.Context, entries []model.Entry) ([]model.Entry, error) {
	return d.upsertEntries(ctx, entries, false)
}

// Recount refreshes a site's aggregate counters.
func (d *DB) Recount(ctx context.Context, siteID int64) error {
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recountTx(ctx, tx, siteID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) upsertEntries(ctx context.Context, entries []model.Entry, recount bool) ([]model.Entry, error) {
	if len(entries) == 0 {
		return []model.Entry{}, nil
	}
	defer d.lockWrite()()
	out := make([]model.Entry, len(entries))
	err := d.withBusyRetry(ctx, func() error {
		tx, err := d.sql.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO entries(site_id,parent_id,name,normalized_path,url,type,size,modified_at,etag,last_modified,content_type,extension,removed,created_at,updated_at,last_seen_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(site_id,normalized_path) DO UPDATE SET parent_id=excluded.parent_id,name=excluded.name,url=excluded.url,type=excluded.type,size=excluded.size,modified_at=excluded.modified_at,etag=excluded.etag,last_modified=excluded.last_modified,content_type=excluded.content_type,extension=excluded.extension,removed=excluded.removed,updated_at=excluded.updated_at,last_seen_at=excluded.last_seen_at RETURNING id,created_at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		attemptOut := make([]model.Entry, len(entries))
		for i, e := range entries {
			if e.SiteID == 0 || e.NormalizedPath == "" || e.URL == "" {
				return fmt.Errorf("entry %d missing required fields", i)
			}
			if e.Type == "" {
				e.Type = model.EntryTypeUnknown
			}
			now := time.Now().UTC()
			if e.CreatedAt.IsZero() {
				e.CreatedAt = now
			}
			if e.UpdatedAt.IsZero() {
				e.UpdatedAt = now
			}
			if e.LastSeenAt.IsZero() {
				e.LastSeenAt = now
			}
			if e.Extension == "" && e.Type == model.EntryTypeFile {
				e.Extension = strings.ToLower(filepath.Ext(e.Name))
			}
			if err := stmt.QueryRowContext(ctx, e.SiteID, e.ParentID, e.Name, e.NormalizedPath, e.URL, e.Type, e.Size, nullableTime(e.ModifiedAt), e.ETag, e.LastModified, e.ContentType, e.Extension, boolInt(e.Removed), unix(e.CreatedAt), unix(e.UpdatedAt), unix(e.LastSeenAt)).Scan(&e.ID, newUnixTime(&e.CreatedAt)); err != nil {
				return fmt.Errorf("upsert %s: %w", e.NormalizedPath, err)
			}
			attemptOut[i] = e
		}
		if recount {
			if err = recountTx(ctx, tx, entries[0].SiteID); err != nil {
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		out = attemptOut
		return nil
	})
	return out, err
}
func recountTx(ctx context.Context, tx *sql.Tx, siteID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE sites SET entry_count=(SELECT COUNT(*) FROM entries WHERE site_id=? AND removed=0),file_count=(SELECT COUNT(*) FROM entries WHERE site_id=? AND removed=0 AND type='file'),directory_count=(SELECT COUNT(*) FROM entries WHERE site_id=? AND removed=0 AND type='directory'),total_size=COALESCE((SELECT SUM(size) FROM entries WHERE site_id=? AND removed=0 AND type='file'),0),updated_at=? WHERE id=?`, siteID, siteID, siteID, siteID, unix(time.Now()), siteID)
	return err
}
func (d *DB) MarkEntriesRemovedBefore(ctx context.Context, siteID int64, seen time.Time) error {
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE entries SET removed=1,updated_at=? WHERE site_id=? AND normalized_path<>'/' AND last_seen_at<?`, unix(time.Now()), siteID, unix(seen)); err != nil {
		return err
	}
	if err = recountTx(ctx, tx, siteID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) StartCrawlRun(ctx context.Context, run *model.CrawlRun) error {
	if run == nil || run.SiteID == 0 {
		return errors.New("invalid crawl run")
	}
	if run.Mode == "" {
		run.Mode = model.CrawlModeNormal
	}
	if run.Status == "" {
		run.Status = model.ScanStatusRunning
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO crawl_runs(site_id,mode,status,started_at) VALUES(?,?,?,?)`, run.SiteID, run.Mode, run.Status, unix(run.StartedAt))
	if err != nil {
		return err
	}
	run.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET scan_status=?,updated_at=? WHERE id=?`, run.Status, unix(time.Now()), run.SiteID); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *DB) FinishCrawlRun(ctx context.Context, run *model.CrawlRun) error {
	if run == nil || run.ID == 0 {
		return errors.New("invalid crawl run")
	}
	if run.FinishedAt == nil {
		t := time.Now().UTC()
		run.FinishedAt = &t
	}
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE crawl_runs SET status=?,finished_at=?,directories=?,files=?,bytes=?,error_count=?,failure_reason=? WHERE id=?`, run.Status, nullableTime(run.FinishedAt), run.Directories, run.Files, run.Bytes, run.ErrorCount, run.FailureReason, run.ID)
	if err = affected(res, err); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sites SET scan_status=?,last_crawled_at=?,updated_at=? WHERE id=?`, run.Status, nullableTime(run.FinishedAt), unix(time.Now()), run.SiteID); err != nil {
		return err
	}
	return tx.Commit()
}
func (d *DB) AddCrawlError(ctx context.Context, e *model.CrawlError) error {
	if e == nil || e.RunID == 0 || e.SiteID == 0 || e.Message == "" {
		return errors.New("invalid crawl error")
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	defer d.lockWrite()()
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO crawl_errors(run_id,site_id,url,path,operation,message,created_at) VALUES(?,?,?,?,?,?,?)`, e.RunID, e.SiteID, e.URL, e.Path, e.Operation, e.Message, unix(e.CreatedAt))
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE crawl_runs SET error_count=error_count+1 WHERE id=?`, e.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ListCrawlErrors(ctx context.Context, siteID int64, limit int) ([]model.CrawlError, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT id,run_id,site_id,url,path,operation,message,created_at FROM crawl_errors WHERE site_id=? ORDER BY id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CrawlError
	for rows.Next() {
		var item model.CrawlError
		var created int64
		if err := rows.Scan(&item.ID, &item.RunID, &item.SiteID, &item.URL, &item.Path, &item.Operation, &item.Message, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = fromUnix(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

// SaveScanCheckpoint durably records how far a bucket scan has progressed so an
// interrupted run can resume from cursor instead of re-listing from the start.
// cursor is the greatest object key safely committed to the index; startedAt is
// the logical start time of the scan, preserved across resumes so reconciliation
// never reaps entries indexed by an earlier segment of the same scan.
func (d *DB) SaveScanCheckpoint(ctx context.Context, siteID int64, cursor string, startedAt time.Time) error {
	if siteID == 0 {
		return errors.New("invalid site id")
	}
	defer d.lockWrite()()
	return d.withBusyRetry(ctx, func() error {
		_, err := d.sql.ExecContext(ctx, `INSERT INTO scan_checkpoints(site_id,cursor,started_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(site_id) DO UPDATE SET cursor=excluded.cursor,updated_at=excluded.updated_at`, siteID, cursor, unix(startedAt), unix(time.Now()))
		return err
	})
}

// ScanCheckpoint returns the saved resume cursor and original scan start time for
// a site. ok is false when no checkpoint exists, meaning a scan should start from
// the beginning.
func (d *DB) ScanCheckpoint(ctx context.Context, siteID int64) (cursor string, startedAt time.Time, ok bool, err error) {
	row := d.sql.QueryRowContext(ctx, `SELECT cursor,started_at FROM scan_checkpoints WHERE site_id=?`, siteID)
	var started int64
	switch err = row.Scan(&cursor, &started); {
	case errors.Is(err, sql.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, err
	default:
		return cursor, fromUnix(started), true, nil
	}
}

// ClearScanCheckpoint removes a site's resume cursor once a scan completes.
func (d *DB) ClearScanCheckpoint(ctx context.Context, siteID int64) error {
	defer d.lockWrite()()
	return d.withBusyRetry(ctx, func() error {
		_, err := d.sql.ExecContext(ctx, `DELETE FROM scan_checkpoints WHERE site_id=?`, siteID)
		return err
	})
}

// ListDownloads returns recent download records for a site, newest first.
func (d *DB) ListDownloads(ctx context.Context, siteID int64, limit int) ([]model.Download, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT id,site_id,entry_id,source_url,destination,status,bytes_done,total_bytes,error,created_at,updated_at,completed_at FROM downloads WHERE site_id=? ORDER BY updated_at DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Download
	for rows.Next() {
		var x model.Download
		var total, completed sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&x.ID, &x.SiteID, &x.EntryID, &x.SourceURL, &x.Destination, &x.Status, &x.BytesDone, &total, &x.Error, &created, &updated, &completed); err != nil {
			return nil, err
		}
		if total.Valid {
			x.TotalBytes = &total.Int64
		}
		x.CreatedAt, x.UpdatedAt, x.CompletedAt = fromUnix(created), fromUnix(updated), fromNullTime(completed)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (d *DB) UpsertDownload(ctx context.Context, x *model.Download) error {
	if x == nil || x.SiteID == 0 || x.EntryID == 0 || x.Destination == "" {
		return errors.New("invalid download")
	}
	if x.Status == "" {
		x.Status = model.DownloadPending
	}
	now := time.Now().UTC()
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now
	}
	x.UpdatedAt = now
	unlock := d.lockWrite()
	defer unlock()
	_, err := d.sql.ExecContext(ctx, `INSERT INTO downloads(site_id,entry_id,source_url,destination,status,bytes_done,total_bytes,error,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(entry_id,destination) DO UPDATE SET source_url=excluded.source_url,status=excluded.status,bytes_done=excluded.bytes_done,total_bytes=excluded.total_bytes,error=excluded.error,updated_at=excluded.updated_at,completed_at=excluded.completed_at`, x.SiteID, x.EntryID, x.SourceURL, x.Destination, x.Status, x.BytesDone, x.TotalBytes, x.Error, unix(x.CreatedAt), unix(x.UpdatedAt), nullableTime(x.CompletedAt))
	if err != nil {
		return err
	}
	return d.sql.QueryRowContext(ctx, `SELECT id,created_at FROM downloads WHERE entry_id=? AND destination=?`, x.EntryID, x.Destination).Scan(&x.ID, newUnixTime(&x.CreatedAt))
}

type unixTimeScanner struct{ dst *time.Time }

func newUnixTime(dst *time.Time) *unixTimeScanner { return &unixTimeScanner{dst} }
func (s *unixTimeScanner) Scan(src any) error {
	n, ok := src.(int64)
	if !ok {
		return fmt.Errorf("invalid time %T", src)
	}
	*s.dst = fromUnix(n)
	return nil
}
func unix(t time.Time) int64     { return t.UTC().UnixNano() }
func fromUnix(n int64) time.Time { return time.Unix(0, n).UTC() }
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return unix(*t)
}
func fromNullTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := fromUnix(n.Int64)
	return &t
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
