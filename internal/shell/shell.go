package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/model"
	"github.com/m1r3dk/dirhop/internal/output"
)

// Exec runs one command line (already split) against a session using the same
// implementation as the one-shot CLI.
type Exec func(ctx context.Context, out io.Writer, session string, args []string) error

var commands = []string{"ls", "cd", "pwd", "tree", "stat", "cat", "du", "find", "search", "download", "refresh", "info", "urls", "errors", "sessions", "use", "clear", "help", "exit", "quit"}

// Commands whose first argument is a remote path, for completion.
var pathCommands = map[string]bool{"ls": true, "cd": true, "tree": true, "stat": true, "cat": true, "du": true, "download": true, "urls": true}

type state struct {
	app  *app.App
	site *model.Site
	out  io.Writer
	exec Exec
}

// Run starts the interactive shell. Shell-native commands (session switching,
// help, clear, exit) are handled here; everything else goes through exec.
func Run(ctx context.Context, application *app.App, site *model.Site, in io.Reader, out io.Writer, exec Exec) error {
	s := &state{app: application, site: site, out: out, exec: exec}
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
	history := loadHistory(s.app.Config.Paths.History)
	t.History = history
	defer history.save()
	t.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' {
			return "", 0, false
		}
		return s.complete(ctx, line, pos)
	}
	// Command output must go through the terminal so raw mode gets \r\n.
	s.out = t
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
		if s.handle(ctx, line) {
			return nil
		}
	}
}

func (s *state) runScanner(ctx context.Context, in io.Reader) error {
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(s.out, s.prompt())
		if !scanner.Scan() {
			fmt.Fprintln(s.out)
			return scanner.Err()
		}
		if s.handle(ctx, scanner.Text()) {
			return nil
		}
	}
}

// handle executes one line and reports whether the shell should exit.
func (s *state) handle(ctx context.Context, line string) bool {
	stop, err := s.execute(ctx, line)
	if err != nil {
		fmt.Fprintf(s.out, "error: %v\n", err)
	}
	return stop
}

func (s *state) prompt() string {
	if current, err := s.app.DB.SiteByID(context.Background(), s.site.ID); err == nil {
		s.site = current
	}
	return fmt.Sprintf("%s:%s > ", s.site.Name, s.site.CWD)
}

func (s *state) execute(ctx context.Context, line string) (bool, error) {
	args, err := Split(strings.TrimSpace(line))
	if err != nil || len(args) == 0 {
		return false, err
	}
	switch cmd := strings.ToLower(args[0]); cmd {
	case "exit", "quit":
		return true, nil
	case "clear":
		fmt.Fprint(s.out, "\x1b[2J\x1b[H")
	case "help":
		fmt.Fprintln(s.out, helpText)
	case "sessions":
		sites, err := s.app.Sessions.List(ctx)
		if err != nil {
			return false, err
		}
		w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "\t#\tNAME\tFILES\tSIZE\tPATH")
		for i, site := range sites {
			mark := " "
			if site.ID == s.site.ID {
				mark = "*"
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%s\n", mark, i+1, site.Name, site.FileCount, output.Size(site.TotalSize), site.CWD)
		}
		w.Flush()
	case "use":
		if len(args) != 2 {
			return false, fmt.Errorf("usage: use <number|name>")
		}
		site, err := s.resolveSession(ctx, args[1])
		if err != nil {
			return false, err
		}
		if _, err := s.app.Sessions.Use(ctx, site.Name); err != nil {
			return false, fmt.Errorf("unknown session %q", args[1])
		}
		s.site = site
		fmt.Fprintf(s.out, "Session changed: %s\n", site.Name)
	default:
		if s.exec == nil {
			return false, fmt.Errorf("unknown command %q; type help", cmd)
		}
		if !contains(commands, cmd) {
			return false, fmt.Errorf("unknown command %q; type help", cmd)
		}
		if cmd == "cd" && len(args) == 1 {
			args = append(args, "/")
		}
		return false, s.exec(ctx, s.out, s.site.Name, args)
	}
	return false, nil
}

// resolveSession turns a "use" argument into a site. A bare positive integer is
// treated as a 1-based row in the "sessions" listing (name order), so users can
// switch with "use 3" instead of typing a long generated name. Anything else is
// passed to the normal resolver, which accepts a session name or numeric ID.
func (s *state) resolveSession(ctx context.Context, arg string) (*model.Site, error) {
	if n, err := strconv.Atoi(strings.TrimSpace(arg)); err == nil && n >= 1 {
		sites, err := s.app.Sessions.List(ctx)
		if err != nil {
			return nil, err
		}
		if n > len(sites) {
			return nil, fmt.Errorf("no session #%d (there are %d; run `sessions`)", n, len(sites))
		}
		return &sites[n-1], nil
	}
	site, err := s.app.Sessions.Resolve(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("unknown session %q", arg)
	}
	return site, nil
}

const helpText = `NAVIGATION
  ls [-l] [-h] [--sort name|size|date] [path]   List directory
  cd <path>                                     Change directory (.., /, relative)
  pwd                                           Show current directory
  tree [--depth N] [--dirs-only] [path]         Display directory tree
  stat <path>                                   Show metadata
  cat <file...>                                 Print remote file contents
  du [path]                                     Indexed disk usage

SEARCH
  find [glob] [--ext E] [--size >1GB] [--regex R] [--modified-after DATE] [--type file|directory]
  search <text>                                 Search names and paths
  urls [--files-only|--dirs-only] [--ext E]     Print indexed URLs

TRANSFER
  download <path...> [--segments N] [--output DIR]

SESSION
  sessions            Show sessions (numbered)
  use <number|name>   Switch session by row number or name
  refresh [--full]    Re-index the current session
  info                Session details
  errors              Crawl errors

SHELL
  clear  help  exit  (Ctrl+D exits, Tab completes, Up/Down history)

Quote paths with spaces: cd "my dir"`

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
		head := strings.ToLower(strings.Fields(prefix)[0])
		switch {
		case head == "use":
			sites, _ := s.app.Sessions.List(ctx)
			for _, site := range sites {
				if strings.HasPrefix(site.Name, word) {
					matches = append(matches, site.Name)
				}
			}
		case pathCommands[head]:
			matches, _ = s.app.FS(s.site).Complete(ctx, word)
			for i, m := range matches {
				if strings.ContainsAny(m, " \t\"'\\") {
					matches[i] = strings.ReplaceAll(m, " ", `\ `)
				}
			}
		}
	}
	if len(matches) == 0 {
		return line, pos, true
	}
	sort.Strings(matches)
	replacement := matches[0]
	if len(matches) > 1 {
		replacement = commonPrefix(matches)
		if replacement == word {
			fmt.Fprintf(s.out, "%s\n", strings.Join(matches, "  "))
			return line, pos, true
		}
	} else if !strings.HasSuffix(replacement, "/") {
		replacement += " "
	}
	return line[:start] + replacement + line[pos:], start + len(replacement), true
}

func commonPrefix(items []string) string {
	p := items[0]
	for _, item := range items[1:] {
		for !strings.HasPrefix(item, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
