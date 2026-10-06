package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/model"
)

var ErrNoActive = errors.New("no active session")

// Manager owns site/session lifecycle. Selecting a session does not activate it
// unless the caller explicitly calls Use or passes activate=true to Open.
type Manager struct{ db *database.DB }

func New(db *database.DB) *Manager { return &Manager{db: db} }

// CanonicalURL normalizes directory identity while preserving meaningful query parameters.
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", errors.New("URL has no hostname")
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u.Host = host
	u.User = nil
	u.Fragment = ""
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", fmt.Errorf("decode URL path: %w", err)
	}
	clean := path.Clean("/" + decoded)
	if !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	u.Path = clean
	u.RawPath = ""
	q := u.Query()
	for _, k := range []string{"C", "O", "F", "S", "sort", "order"} {
		q.Del(k)
		q.Del(strings.ToLower(k))
		q.Del(strings.ToUpper(k))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Open returns an existing canonical URL session or creates it.
func (m *Manager) Open(ctx context.Context, rawURL, name string, activate bool) (*model.Site, bool, error) {
	canonical, err := CanonicalURL(rawURL)
	if err != nil {
		return nil, false, err
	}
	if existing, err := m.db.SiteByCanonicalURL(ctx, canonical); err == nil {
		if activate {
			if err = m.db.SetActiveSite(ctx, existing.ID); err != nil {
				return nil, false, err
			}
		}
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	u, _ := url.Parse(canonical)
	if strings.TrimSpace(name) == "" {
		name = m.availableName(ctx, u)
	} else {
		name = sanitizeName(name)
		if name == "" {
			return nil, false, errors.New("invalid session name")
		}
		if _, err = m.db.SiteByName(ctx, name); err == nil {
			return nil, false, fmt.Errorf("session name %q already exists", name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, err
		}
	}
	s := &model.Site{Name: name, OriginalURL: rawURL, CanonicalURL: canonical, Hostname: u.Hostname(), CWD: "/", ScanStatus: model.ScanStatusPending, CrawlConcurrency: 8}
	if err = m.db.CreateSite(ctx, s); err != nil {
		return nil, false, err
	}
	if activate {
		if err = m.db.SetActiveSite(ctx, s.ID); err != nil {
			return nil, false, err
		}
	}
	return s, true, nil
}

func (m *Manager) availableName(ctx context.Context, u *url.URL) string {
	base := preferredBase(u)
	if base == "" {
		base = "session"
	}
	candidate := base
	for i := 2; ; i++ {
		_, err := m.db.SiteByName(ctx, candidate)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// preferredBase is the session name stem for a URL. Recognized buckets use just
// the bucket (or container) name, e.g. "wustl" for
// wustl.s3-us-west-2.amazonaws.com, so sessions read cleanly. Anything else
// falls back to host + last path segment.
func preferredBase(u *url.URL) string {
	if target, ok := bucket.Detect(u.String()); ok && target.Bucket != "" {
		if base := sanitizeName(target.Bucket); base != "" {
			return base
		}
	}
	return sanitizeName(u.Hostname() + "-" + strings.Trim(path.Base(strings.TrimSuffix(u.Path, "/")), "/"))
}

// NormalizeNames renames every session to its preferred bucket-based name,
// keeping names unique. It is a two-phase rename (to temporary names first) so
// no transient UNIQUE collision can occur when two old names map to the same
// bucket base. Returns the number of sessions whose name changed.
func (m *Manager) NormalizeNames(ctx context.Context) (int, error) {
	sites, err := m.db.ListSites(ctx)
	if err != nil {
		return 0, err
	}
	// Compute the desired final name for each site, deduping across the whole set.
	used := map[string]bool{}
	desired := make(map[int64]string, len(sites))
	for _, s := range sites {
		u, err := url.Parse(s.CanonicalURL)
		if err != nil {
			desired[s.ID] = s.Name
			used[s.Name] = true
			continue
		}
		base := preferredBase(u)
		if base == "" {
			base = "session"
		}
		name := base
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[name] = true
		desired[s.ID] = name
	}
	// Phase 1: move every site that needs a new name to a unique temporary name,
	// so the final names are all free regardless of rename order.
	var changing []model.Site
	for _, s := range sites {
		if desired[s.ID] != s.Name {
			changing = append(changing, s)
		}
	}
	for _, s := range changing {
		tmp := fmt.Sprintf("__dirhop_migrating_%d", s.ID)
		if err := m.db.RenameSite(ctx, s.ID, tmp); err != nil {
			return 0, err
		}
	}
	// Phase 2: assign the final names.
	for _, s := range changing {
		if err := m.db.RenameSite(ctx, s.ID, desired[s.ID]); err != nil {
			return 0, err
		}
	}
	return len(changing), nil
}

var dashRE = regexp.MustCompile(`-+`)

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(dashRE.ReplaceAllString(b.String(), "-"), "-")
}

func (m *Manager) Resolve(ctx context.Context, selector string) (*model.Site, error) {
	if strings.TrimSpace(selector) == "" {
		s, err := m.db.ActiveSite(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNoActive
		}
		return s, err
	}
	if id, err := parseID(selector); err == nil {
		if s, e := m.db.SiteByID(ctx, id); e == nil {
			return s, nil
		}
	}
	return m.db.SiteByName(ctx, selector)
}
func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("not an id")
	}
	return id, nil
}
func (m *Manager) Active(ctx context.Context) (*model.Site, error) { return m.Resolve(ctx, "") }
func (m *Manager) Use(ctx context.Context, selector string) (*model.Site, error) {
	s, err := m.Resolve(ctx, selector)
	if err != nil {
		return nil, err
	}
	if err = m.db.SetActiveSite(ctx, s.ID); err != nil {
		return nil, err
	}
	return s, nil
}
func (m *Manager) List(ctx context.Context) ([]model.Site, error) { return m.db.ListSites(ctx) }
func (m *Manager) Rename(ctx context.Context, selector, name string) (*model.Site, error) {
	s, err := m.Resolve(ctx, selector)
	if err != nil {
		return nil, err
	}
	name = sanitizeName(name)
	if name == "" {
		return nil, errors.New("invalid session name")
	}
	if err = m.db.RenameSite(ctx, s.ID, name); err != nil {
		return nil, err
	}
	return m.db.SiteByID(ctx, s.ID)
}
func (m *Manager) Remove(ctx context.Context, selector string) error {
	s, err := m.Resolve(ctx, selector)
	if err != nil {
		return err
	}
	return m.db.DeleteSite(ctx, s.ID)
}
func (m *Manager) ClearActive(ctx context.Context) error { return m.db.ClearActiveSite(ctx) }
