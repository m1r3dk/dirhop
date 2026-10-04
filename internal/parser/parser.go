// Package parser detects and parses common HTTP directory listing formats.
package parser

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var (
	errUnsafeURL          = errors.New("parser: unsafe listing URL")
	ErrUnsupportedListing = errors.New("unsupported directory listing")
	sizePattern           = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)([kmgtpe]?)(?:i?b|bytes)?$`)
	unitPattern           = regexp.MustCompile(`(?i)^([kmgtpe]i?b?|b|bytes)$`)
	numberPattern         = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
)

// Entry is one link discovered in a directory listing.
type Entry struct {
	Name     string
	URL      *url.URL
	IsDir    bool
	Size     *int64
	Modified *time.Time
}

// Document is a parsed HTML listing and its response headers.
type Document struct {
	Root   *html.Node
	Header http.Header
	text   string
}

// DirectoryParser is implemented by each recognized listing format.
type DirectoryParser interface {
	Name() string
	Score(*Document) int
	Parse(*Document, *url.URL) ([]Entry, error)
}

type listingParser struct {
	name  string
	score func(*Document) int
}

// ParseDocument parses a response body once for detector and parser reuse.
func ParseDocument(reader io.Reader, header http.Header) (*Document, error) {
	if reader == nil {
		return nil, errors.New("parser: nil document reader")
	}
	root, err := html.Parse(reader)
	if err != nil {
		return nil, fmt.Errorf("parser: parse HTML: %w", err)
	}
	if header == nil {
		header = make(http.Header)
	}
	return &Document{Root: root, Header: header.Clone(), text: strings.ToLower(nodeText(root))}, nil
}

// Parsers returns the detectors in stable precedence order.
func Parsers() []DirectoryParser {
	return []DirectoryParser{
		listingParser{name: "apache", score: scoreApache},
		listingParser{name: "nginx", score: scoreNginx},
		listingParser{name: "python", score: scorePython},
		listingParser{name: "iis", score: textScore(90, "[to parent directory]", "&lt;dir&gt;", "<dir>")},
		listingParser{name: "lighttpd", score: textScore(80, "lighttpd")},
		listingParser{name: "caddy", score: textScore(80, "caddy")},
		listingParser{name: "generic", score: func(document *Document) int {
			if document != nil && hasElement(document.Root, "a") {
				return 1
			}
			return 0
		}},
	}
}

// Detect selects the highest-scoring parser.
func Detect(document *Document) DirectoryParser {
	var selected DirectoryParser
	best := -1
	for _, candidate := range Parsers() {
		if score := candidate.Score(document); score > best {
			selected, best = candidate, score
		}
	}
	return selected
}

// Parse parses HTML, detects its listing format, and returns safe links relative
// to pageURL. The crawler must still validate links against its canonical root.
func Parse(reader io.Reader, header http.Header, pageURL *url.URL) (string, []Entry, error) {
	if pageURL == nil {
		return "", nil, errors.New("parser: nil page URL")
	}
	document, err := ParseDocument(reader, header)
	if err != nil {
		return "", nil, err
	}
	selected := Detect(document)
	if selected == nil || selected.Score(document) <= 0 {
		return "", nil, ErrUnsupportedListing
	}
	entries, err := selected.Parse(document, pageURL)
	if err != nil {
		return selected.Name(), nil, err
	}
	return selected.Name(), entries, nil
}

func (parser listingParser) Name() string { return parser.name }
func (parser listingParser) Score(document *Document) int {
	if document == nil || parser.score == nil {
		return 0
	}
	return parser.score(document)
}
func (parser listingParser) Parse(document *Document, pageURL *url.URL) ([]Entry, error) {
	if document == nil || document.Root == nil || pageURL == nil {
		return nil, errors.New("parser: invalid parse input")
	}
	return parseAnchors(document.Root, pageURL)
}

// ResolveURL resolves a listing link and enforces HTTP(S), same-origin, and
// canonical base-path confinement. Directory URLs are normalized with a slash.
func ResolveURL(baseURL, pageURL *url.URL, href string, directory bool) (*url.URL, error) {
	if baseURL == nil || pageURL == nil {
		return nil, errUnsafeURL
	}
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || isParentReference(href) {
		return nil, errUnsafeURL
	}
	reference, err := url.Parse(href)
	if err != nil || reference.Scheme != "" && reference.Scheme != "http" && reference.Scheme != "https" {
		return nil, errUnsafeURL
	}
	if reference.Path == "" && reference.RawPath == "" {
		return nil, errUnsafeURL
	}
	if isSortQuery(reference.RawQuery) {
		return nil, errUnsafeURL
	}

	resolved := pageURL.ResolveReference(reference)
	resolved.Fragment = ""
	resolved.Scheme = strings.ToLower(resolved.Scheme)
	resolved.Host = canonicalHost(resolved)
	baseScheme := strings.ToLower(baseURL.Scheme)
	baseHost := canonicalHost(baseURL)
	if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Scheme != baseScheme || resolved.Host != baseHost {
		return nil, errUnsafeURL
	}

	basePath := cleanDirectoryPath(baseURL.EscapedPath())
	resolvedPath := cleanEscapedPath(resolved.EscapedPath(), directory)
	if resolvedPath != strings.TrimSuffix(basePath, "/") && !strings.HasPrefix(resolvedPath, basePath) {
		return nil, errUnsafeURL
	}
	resolved.Path, err = url.PathUnescape(resolvedPath)
	if err != nil {
		return nil, errUnsafeURL
	}
	resolved.RawPath = resolvedPath
	if resolved.EscapedPath() == resolved.Path {
		resolved.RawPath = ""
	}
	if directory && !strings.HasSuffix(resolved.Path, "/") {
		resolved.Path += "/"
		if resolved.RawPath != "" {
			resolved.RawPath += "/"
		}
	}
	return resolved, nil
}

func parseAnchors(root *html.Node, pageURL *url.URL) ([]Entry, error) {
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "a") {
			href := attribute(node, "href")
			label := strings.TrimSpace(nodeText(node))
			directory := linkLooksDirectory(href, label)
			resolved, err := ResolveURL(pageURL, pageURL, href, directory)
			if err == nil {
				name := entryName(resolved, label, directory)
				if name != "" && !isParentLabel(name) {
					key := resolved.String()
					if _, exists := seen[key]; !exists {
						seen[key] = struct{}{}
						entry := Entry{Name: name, URL: resolved, IsDir: directory}
						entry.Size, entry.Modified = parseMetadata(node)
						entries = append(entries, entry)
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return entries, nil
}

func scoreApache(document *Document) int {
	score := 0
	if strings.Contains(document.text, "apache") {
		score += 55
	}
	if hasElement(document.Root, "table") && strings.Contains(document.text, "index of") {
		score += 40
	}
	return score
}

func scoreNginx(document *Document) int {
	score := 0
	if strings.Contains(document.text, "nginx") {
		score += 55
	}
	if hasElement(document.Root, "pre") && strings.Contains(document.text, "index of") {
		score += 40
	}
	return score
}

func scorePython(document *Document) int {
	if strings.Contains(document.text, "directory listing for") {
		return 90
	}
	return 0
}

func parseMetadata(anchor *html.Node) (*int64, *time.Time) {
	fields := strings.Fields(rowText(anchor))
	modified, rest := extractTime(fields)
	return extractSize(rest), modified
}

// rowText returns the metadata text belonging to one listing entry: the rest
// of its table row (Apache, lighttpd, Caddy), or the text on the same line of
// a preformatted block (nginx after the link, IIS before it).
func rowText(anchor *html.Node) string {
	for n := anchor.Parent; n != nil; n = n.Parent {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "tr") {
			var b strings.Builder
			for cell := n.FirstChild; cell != nil; cell = cell.NextSibling {
				if !containsNode(cell, anchor) {
					b.WriteString(nodeText(cell))
					b.WriteByte(' ')
				}
			}
			return b.String()
		}
	}
	var before, after string
	for s := anchor.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode && (strings.EqualFold(s.Data, "a") || strings.EqualFold(s.Data, "br")) {
			break
		}
		text := siblingText(s)
		if i := strings.LastIndex(text, "\n"); i >= 0 {
			before = text[i+1:] + before
			break
		}
		before = text + before
	}
	for s := anchor.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode && (strings.EqualFold(s.Data, "a") || strings.EqualFold(s.Data, "br")) {
			break
		}
		text := siblingText(s)
		if i := strings.Index(text, "\n"); i >= 0 {
			after += text[:i]
			break
		}
		after += text
	}
	return before + " " + after
}

func siblingText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	return nodeText(n)
}

func containsNode(root, target *html.Node) bool {
	if root == target {
		return true
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if containsNode(c, target) {
			return true
		}
	}
	return false
}

var timeLayouts = []string{
	"2006-01-02 15:04:05", "2006-01-02 15:04", "02-Jan-2006 15:04:05", "02-Jan-2006 15:04",
	"2006-Jan-02 15:04:05", "1/2/2006 3:04 PM", "Monday, January 2, 2006 3:04 PM",
	"Mon, 02 Jan 2006 15:04:05 MST", time.RFC3339,
}

// extractTime finds the first timestamp spanning 1-6 consecutive fields and
// returns the remaining fields so date digits are never mistaken for sizes.
func extractTime(fields []string) (*time.Time, []string) {
	for width := 6; width >= 1; width-- {
		for start := 0; start+width <= len(fields); start++ {
			candidate := strings.Join(fields[start:start+width], " ")
			for _, layout := range timeLayouts {
				if t, err := time.ParseInLocation(layout, candidate, time.Local); err == nil {
					rest := append(append([]string{}, fields[:start]...), fields[start+width:]...)
					return &t, rest
				}
			}
		}
	}
	return nil, fields
}

// extractSize returns the last size-shaped field ("1.4K", "38M", "4120",
// "1.4 KiB"). Directory markers and "-" yield nil.
func extractSize(fields []string) *int64 {
	for i := len(fields) - 1; i >= 0; i-- {
		token := fields[i]
		if i+1 < len(fields) && unitPattern.MatchString(fields[i+1]) && numberPattern.MatchString(token) {
			token += fields[i+1]
		}
		m := sizePattern.FindStringSubmatch(token)
		if m == nil {
			continue
		}
		if n, ok := parseSize(m[1], m[2]); ok {
			return &n
		}
	}
	return nil
}

func parseSize(number, unit string) (int64, bool) {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, false
	}
	power := strings.Index(" kmgtpe", strings.ToLower(unit))
	if power < 0 {
		return 0, false
	}
	for i := 0; i < power; i++ {
		value *= 1024
	}
	return int64(value), true
}

func linkLooksDirectory(href, label string) bool {
	pathPart := href
	if parsed, err := url.Parse(href); err == nil {
		pathPart = parsed.Path
	}
	return strings.HasSuffix(pathPart, "/") || strings.HasSuffix(strings.TrimSpace(label), "/")
}

// entryName uses the decoded last URL segment. Link labels are display text:
// Apache truncates long names ("long-na..>"), and generic pages use captions
// ("Annual report"), so they cannot serve as filesystem names.
func entryName(target *url.URL, label string, directory bool) string {
	name := path.Base(strings.TrimSuffix(target.EscapedPath(), "/"))
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	if name == "" || name == "/" || name == "." {
		name = strings.TrimSuffix(strings.TrimSpace(label), "/")
	}
	return name
}

func canonicalHost(target *url.URL) string {
	hostname := strings.ToLower(target.Hostname())
	port := target.Port()
	if port == "" || target.Scheme == "http" && port == "80" || target.Scheme == "https" && port == "443" {
		return hostname
	}
	return netJoinHostPort(hostname, port)
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func cleanDirectoryPath(escaped string) string {
	cleaned := cleanEscapedPath(escaped, true)
	if !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	return cleaned
}

func cleanEscapedPath(escaped string, directory bool) string {
	if escaped == "" {
		escaped = "/"
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(escaped, "/"))
	if directory && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

func isParentReference(href string) bool {
	parsed, err := url.Parse(href)
	if err != nil {
		return true
	}
	clean := strings.TrimSpace(parsed.Path)
	return clean == ".." || clean == "../" || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../")
}

func isSortQuery(raw string) bool {
	if raw == "" {
		return false
	}
	values, err := url.ParseQuery(strings.ReplaceAll(raw, ";", "&"))
	if err != nil {
		return false
	}
	for key := range values {
		switch strings.ToUpper(key) {
		case "C", "O", "N", "M", "S", "D":
			return true
		}
	}
	return false
}

func isParentLabel(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == ".." || name == "parent directory" || name == "[to parent directory]"
}

func attribute(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func hasElement(node *html.Node, name string) bool {
	if node == nil {
		return false
	}
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, name) {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasElement(child, name) {
			return true
		}
	}
	return false
}

func nodeText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}
