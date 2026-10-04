package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/m1r3dk/dirclone/internal/app"
	"github.com/m1r3dk/dirclone/internal/downloader"
	"github.com/m1r3dk/dirclone/internal/model"
	"github.com/m1r3dk/dirclone/internal/output"
)

var commands = []string{"ls", "cd", "pwd", "tree", "stat", "du", "find", "search", "download", "refresh", "info", "urls", "errors", "sessions", "use", "clear", "help", "exit", "quit"}

type state struct {
	app  *app.App
	site *model.Site
	out  io.Writer
}

// Run starts an interactive remote-filesystem shell. All filesystem operations
// use the same SQLite-backed FS as one-shot commands.
func Run(ctx context.Context, application *app.App, site *model.Site, in io.Reader, out io.Writer) error {
	s := &state{app: application, site: site, out: out}
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		return s.runTerminal(ctx, file, out)
	}
	return s.runScanner(ctx, in)
}

type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }

func (s *state) runTerminal(ctx context.Context, input *os.File, out io.Writer) error {
	old, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(input.Fd()), old)
	t := term.NewTerminal(readWriter{input, out}, s.prompt())
	if w, h, e := term.GetSize(int(input.Fd())); e == nil {
		_ = t.SetSize(w, h)
	}
	t.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' {
			return "", 0, false
		}
		return s.complete(ctx, line, pos)
	}
	for {
		t.SetPrompt(s.prompt())
		line, err := t.ReadLine()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(out)
			return nil
		}
		if err != nil && !errors.Is(err, term.ErrPasteIndicator) {
			return err
		}
		stop, runErr := s.execute(ctx, line)
		if runErr != nil {
			fmt.Fprintf(out, "error: %v\n", runErr)
		}
		if stop {
			return nil
		}
	}
}

func (s *state) runScanner(ctx context.Context, in io.Reader) error {
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(s.out, s.prompt())
		if !scanner.Scan() {
			return scanner.Err()
		}
		stop, err := s.execute(ctx, scanner.Text())
		if err != nil {
			fmt.Fprintf(s.out, "error: %v\n", err)
		}
		if stop {
			return nil
		}
	}
}

func (s *state) prompt() string {
	cwd := s.site.CWD
	if current, err := s.app.DB.SiteByID(context.Background(), s.site.ID); err == nil {
		s.site = current
		cwd = current.CWD
	}
	return fmt.Sprintf("%s:%s > ", s.site.Name, cwd)
}

func (s *state) execute(ctx context.Context, line string) (bool, error) {
	args, err := Split(strings.TrimSpace(line))
	if err != nil {
		return false, err
	}
	if len(args) == 0 {
		return false, nil
	}
	cmd, args := strings.ToLower(args[0]), args[1:]
	fs := s.app.FS(s.site)
	switch cmd {
	case "exit", "quit":
		return true, nil
	case "clear":
		fmt.Fprint(s.out, "\x1b[2J\x1b[H")
		return false, nil
	case "help":
		s.help()
		return false, nil
	case "pwd":
		cwd, err := fs.CWD(ctx)
		if err == nil {
			fmt.Fprintln(s.out, cwd)
		}
		return false, err
	case "cd":
		p := "/"
		if len(args) > 0 {
			p = args[0]
		}
		_, err := fs.ChangeDirectory(ctx, p)
		return false, err
	case "ls":
		p, long, human := shellPath(args, "."), hasFlag(args, "-l"), hasFlag(args, "-h")
		entries, err := fs.List(ctx, p)
		if err != nil {
			return false, err
		}
		w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
		defer w.Flush()
		for _, e := range entries {
			name := entryName(e)
			if !long {
				fmt.Fprintln(w, name)
				continue
			}
			size := output.Raw(entrySize(e))
			if human {
				size = output.Size(entrySize(e))
			}
			fmt.Fprintf(w, "%s\t%s\n", size, name)
		}
		return false, nil
	case "tree":
		p := shellPath(args, ".")
		root, err := fs.Resolve(ctx, p)
		if err != nil {
			return false, err
		}
		fmt.Fprintln(s.out, entryName(*root))
		err = fs.Walk(ctx, p, func(e model.Entry) error {
			if e.ID == root.ID {
				return nil
			}
			relative := strings.TrimPrefix(strings.TrimPrefix(e.NormalizedPath, root.NormalizedPath), "/")
			depth := strings.Count(relative, "/")
			fmt.Fprintf(s.out, "%s└── %s\n", strings.Repeat("    ", depth), entryName(e))
			return nil
		})
		return false, err
	case "stat":
		if len(args) != 1 {
			return false, fmt.Errorf("usage: stat <path>")
		}
		e, err := fs.Stat(ctx, args[0])
		if err != nil {
			return false, err
		}
		fmt.Fprintf(s.out, "Path:          %s\nType:          %s\nSize:          %s\nModified:      %s\nMIME:          %s\nURL:           %s\n", e.NormalizedPath, e.Type, output.Size(entrySize(*e)), shellTime(e.ModifiedAt), e.ContentType, e.URL)
		return false, nil
	case "du":
		p := shellPath(args, ".")
		du, err := fs.DU(ctx, p)
		if err == nil {
			fmt.Fprintf(s.out, "Directories: %d\nFiles:       %d\nSize:        %s\n", du.Directories, du.Files, output.Size(du.Bytes))
		}
		return false, err
	case "find":
		pattern := "*"
		if len(args) > 0 {
			pattern = args[0]
			if !strings.ContainsAny(pattern, "*?[") {
				pattern = "*" + pattern + "*"
			}
		}
		entries, err := fs.Find(ctx, "/", model.FindOptions{Glob: pattern})
		if err != nil {
			return false, err
		}
		printPaths(s.out, entries)
		return false, nil
	case "search":
		if len(args) == 0 {
			return false, fmt.Errorf("usage: search <text>")
		}
		entries, err := fs.Search(ctx, "/", strings.Join(args, " "), 0)
		if err != nil {
			return false, err
		}
		printPaths(s.out, entries)
		return false, nil
	case "urls":
		p := shellPath(args, "/")
		urls, err := fs.URLs(ctx, p, false)
		if err != nil {
			return false, err
		}
		for _, u := range urls {
			fmt.Fprintln(s.out, u)
		}
		return false, nil
	case "download":
		if len(args) == 0 {
			return false, fmt.Errorf("usage: download <path>")
		}
		result, err := s.app.Download(ctx, s.site, args, s.app.Config.DownloadDirectory, downloader.Options{Concurrency: s.app.Config.DownloadWorkers, Retries: s.app.Config.Retries})
		fmt.Fprintf(s.out, "Completed: %d  Skipped: %d  Failed: %d\n", result.Completed, result.Skipped, result.Failed)
		return false, err
	case "refresh":
		err := s.app.Crawl(ctx, s.site, hasFlag(args, "--full"))
		if err == nil {
			s.site, _ = s.app.DB.SiteByID(ctx, s.site.ID)
			fmt.Fprintln(s.out, "Index refreshed.")
		}
		return false, err
	case "info":
		fmt.Fprintf(s.out, "Name: %s\nURL: %s\nFiles: %d\nDirectories: %d\nSize: %s\n", s.site.Name, s.site.CanonicalURL, s.site.FileCount, s.site.DirectoryCount, output.Size(s.site.TotalSize))
		return false, nil
	case "errors":
		items, err := s.app.DB.ListCrawlErrors(ctx, s.site.ID, 100)
		if err != nil {
			return false, err
		}
		for _, e := range items {
			fmt.Fprintf(s.out, "%s\t%s\n", e.Path, e.Message)
		}
		return false, nil
	case "sessions":
		sites, err := s.app.Sessions.List(ctx)
		if err != nil {
			return false, err
		}
		for _, site := range sites {
			mark := " "
			if site.ID == s.site.ID {
				mark = "*"
			}
			fmt.Fprintf(s.out, "%s %-24s %d files\n", mark, site.Name, site.FileCount)
		}
		return false, nil
	case "use":
		if len(args) != 1 {
			return false, fmt.Errorf("usage: use <session>")
		}
		site, err := s.app.Sessions.Use(ctx, args[0])
		if err == nil {
			s.site = site
			fmt.Fprintf(s.out, "Session changed: %s\n", site.Name)
		}
		return false, err
	default:
		return false, fmt.Errorf("unknown command %q; type help", cmd)
	}
}

func (s *state) help() {
	fmt.Fprintln(s.out, `NAVIGATION
  ls          List directory
  cd          Change directory
  pwd         Show current directory
  tree        Display directory tree
  stat        Show metadata
  du          Show indexed disk usage

SEARCH
  find        Find indexed paths
  search      Search indexed metadata
  urls        Print indexed URLs

TRANSFER
  download    Download files or directories explicitly

SESSION
  sessions    Show sessions
  use         Switch session
  refresh     Refresh current session
  info        Show session information
  errors      Show crawl errors

SHELL
  clear       Clear screen
  help        Show help
  exit        Exit dirclone`)
}

func (s *state) complete(ctx context.Context, line string, pos int) (string, int, bool) {
	if pos > len(line) {
		pos = len(line)
	}
	prefix := line[:pos]
	start := strings.LastIndexAny(prefix, " \t") + 1
	word := prefix[start:]
	var matches []string
	if start == 0 {
		for _, cmd := range commands {
			if strings.HasPrefix(cmd, strings.ToLower(word)) {
				matches = append(matches, cmd)
			}
		}
	} else {
		matches, _ = s.app.FS(s.site).Complete(ctx, word)
	}
	if len(matches) == 0 {
		return line, pos, true
	}
	sort.Strings(matches)
	replacement := matches[0]
	if len(matches) > 1 {
		replacement = commonPrefix(matches)
		if replacement == word {
			fmt.Fprintf(s.out, "\n%s\n", strings.Join(matches, "  "))
			return line, pos, true
		}
	}
	updated := line[:start] + replacement + line[pos:]
	return updated, start + len(replacement), true
}

func commonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	p := items[0]
	for _, item := range items[1:] {
		for !strings.HasPrefix(item, p) && p != "" {
			p = p[:len(p)-1]
		}
	}
	return p
}
func shellPath(args []string, fallback string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return fallback
}
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.Contains(arg, flag) {
			return true
		}
	}
	return false
}
func entryName(e model.Entry) string {
	name := e.Name
	if e.NormalizedPath == "/" {
		name = "/"
	}
	if e.IsDir() && name != "/" {
		name += "/"
	}
	return name
}
func entrySize(e model.Entry) int64 {
	if e.Size == nil {
		return -1
	}
	return *e.Size
}
func shellTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}
func printPaths(out io.Writer, entries []model.Entry) {
	for _, e := range entries {
		fmt.Fprintln(out, path.Clean(strings.TrimPrefix(e.NormalizedPath, "/")))
	}
}
