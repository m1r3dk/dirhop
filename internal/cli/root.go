package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/config"
	"github.com/m1r3dk/dirhop/internal/downloader"
	"github.com/m1r3dk/dirhop/internal/filesystem"
	"github.com/m1r3dk/dirhop/internal/model"
	"github.com/m1r3dk/dirhop/internal/output"
	"github.com/m1r3dk/dirhop/internal/shell"
)

type options struct {
	session string
	url     string
	name    string
	config  string
	json    bool
	quiet   bool
	noColor bool
	verbose bool
	debug   bool
}

func Execute() error {
	root, cleanup, err := newRoot(os.Stdout, os.Stderr, configArgument(os.Args[1:]))
	if err != nil {
		return err
	}
	defer cleanup()
	return root.ExecuteContext(context.Background())
}

func newRoot(stdout, stderr io.Writer, configPath string) (*cobra.Command, func(), error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, func() {}, err
	}
	application, err := app.Open(cfg)
	if err != nil {
		return nil, func() {}, err
	}
	return newCommandTree(application, &options{}, stdout, stderr), func() { _ = application.Close() }, nil
}

// newCommandTree builds the full command set. The interactive shell builds a
// fresh tree per input line, so both interfaces share one implementation.
func newCommandTree(application *app.App, optp *options, stdout, stderr io.Writer) *cobra.Command {
	opt := optp
	cfg := application.Config
	runShell := func(ctx context.Context, site *model.Site) error {
		return shell.Run(ctx, application, site, os.Stdin, stdout, shellExec(application))
	}

	// openSessionShell starts the shell for -s NAME, else the active session,
	// else a picker (TTY only).
	openSessionShell := func(ctx context.Context) error {
		site, err := application.Site(ctx, opt.session)
		if errors.Is(err, app.ErrNoSession) && opt.session == "" {
			site, err = pickSession(ctx, application, os.Stdin, stdout)
		}
		if err != nil {
			return err
		}
		return runShell(ctx, site)
	}

	root := &cobra.Command{
		Use:   "dirhop [URL]",
		Short: "Browse HTTP directory listings and public S3/GCS buckets like a local filesystem",
		Long: `dirhop indexes a directory-listing website or public bucket once, stores
its file tree locally, and lets you browse, search, and download from it
like a filesystem - interactively or with one-shot commands.

Crawling fetches directory pages only. Files are downloaded only when you
run "download".`,
		Example: `  dirhop https://example.com/pub/        index (first time) and open the shell
  dirhop shell                           reopen the shell on the active session
  dirhop -s mirror                       open the shell on session "mirror"
  dirhop scan -f urls.txt                index many sites from a file
  dirhop sessions                        list indexed sites
  dirhop -s mirror find "*.iso"          search without touching the network
  dirhop -s mirror download /pub/x.iso   download one file`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 0 {
				if cmd.Flags().Changed("session") {
					return openSessionShell(ctx)
				}
				return cmd.Help()
			}
			if !isHTTPURL(args[0]) {
				return fmt.Errorf("%w: unknown command or URL %q (run `dirhop --help`)", ErrInvalidArguments, args[0])
			}
			site, created, err := application.OpenURL(ctx, args[0], opt.name, true)
			if err != nil {
				return err
			}
			if !opt.quiet {
				if created {
					fmt.Fprintf(stdout, "Index ready: %s (%d files, %s)\n", site.Name, site.FileCount, output.Size(site.TotalSize))
				} else {
					fmt.Fprintf(stdout, "Existing session found: %s (%d files)\n", site.Name, site.FileCount)
				}
			}
			return runShell(ctx, site)
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	// Bad flag spellings are user error, not an internal failure: exit 2.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	})
	var progressDone func()
	root.PersistentPreRun = func(*cobra.Command, []string) {
		application.Progress, progressDone = progressPrinter(stderr, opt.quiet || opt.json)
		if (opt.verbose || opt.debug) && !application.Logging {
			application.HTTP.EnableLogging(stderr)
			application.Logging = true
		}
	}
	root.PersistentPostRun = func(*cobra.Command, []string) {
		if progressDone != nil {
			progressDone()
		}
	}
	flags := root.PersistentFlags()
	flags.StringVarP(&opt.session, "session", "s", opt.session, "session name or ID")
	flags.StringVarP(&opt.config, "config", "c", cfg.Paths.ConfigFile, "configuration file")
	flags.StringVarP(&opt.url, "url", "u", "", "select or create a session by URL for this command")
	flags.StringVarP(&opt.name, "name", "n", "", "custom name when creating a URL session")
	flags.BoolVarP(&opt.json, "json", "j", false, "emit JSON")
	flags.BoolVarP(&opt.quiet, "quiet", "q", opt.quiet, "suppress non-essential output")
	flags.BoolVar(&opt.noColor, "no-color", false, "disable color output")
	flags.BoolVarP(&opt.verbose, "verbose", "v", false, "show verbose diagnostics")
	flags.BoolVar(&opt.debug, "debug", false, "show debug diagnostics")
	flags.IntVar(&application.Workers, "workers", 0, "crawl/listing concurrency for this run (default from config, max 64)")

	addCommands(root, application, opt, stdout)
	root.AddCommand(&cobra.Command{Use: "shell", Short: "Open the interactive shell on the selected or active session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return openSessionShell(cmd.Context())
	}})
	root.AddCommand(&cobra.Command{Use: "open <url>", Short: "Open (or create) a URL session and start the shell", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, _, err := application.OpenURL(cmd.Context(), args[0], opt.name, true)
		if err != nil {
			return err
		}
		return runShell(cmd.Context(), site)
	}})
	return root
}

// shellExec runs one shell line through a fresh command tree pinned to the
// shell's current session.
func shellExec(a *app.App) shell.Exec {
	return func(ctx context.Context, out io.Writer, session string, args []string) error {
		// cd is silent in the shell; the prompt already shows the new directory.
		tree := newCommandTree(a, &options{session: session, quiet: args[0] == "cd"}, out, out)
		tree.SetArgs(args)
		return tree.ExecuteContext(ctx)
	}
}

func addCommands(root *cobra.Command, a *app.App, opt *options, out io.Writer) {
	root.AddCommand(newScan(a, opt, out))
	root.AddCommand(newLS(a, opt, out), newCD(a, opt, out), newPWD(a, opt, out))
	root.AddCommand(newTree(a, opt, out), newStat(a, opt, out), newDU(a, opt, out))
	root.AddCommand(newFind(a, opt, out), newSearch(a, opt, out), newURLs(a, opt, out))
	root.AddCommand(newDownload(a, opt, out), newRefresh(a, opt, out))
	root.AddCommand(newInfo(a, opt, out), newErrors(a, opt, out))
	root.AddCommand(newSessions(a, opt, out), newSession(a, opt, out))
	root.AddCommand(newConfig(a, out))
	root.AddCommand(newDownloads(a, opt, out))
}

func newDownloads(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "downloads", Short: "Show recorded download state for the session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		items, err := a.DB.ListDownloads(cmd.Context(), site.ID, limit)
		if err != nil || opt.json {
			if err == nil {
				err = output.JSON(out, items)
			}
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		defer w.Flush()
		fmt.Fprintln(w, "STATUS\tBYTES\tUPDATED\tDESTINATION")
		for _, d := range items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", d.Status, output.Size(d.BytesDone), d.UpdatedAt.Local().Format("2006-01-02 15:04"), d.Destination)
		}
		return nil
	}}
	cmd.Flags().IntVarP(&limit, "limit", "l", 100, "maximum records")
	return cmd
}

func newScan(a *app.App, opt *options, out io.Writer) *cobra.Command {
	metadata := a.Config.Metadata
	var file string
	cmd := &cobra.Command{
		Use:   "scan [URL...] [-f FILE]",
		Short: "Index one or more URLs (full rescan for existing sessions)",
		Long: `Index one or more directory listings or buckets.

URLs come from arguments and/or -f FILE (one per line; "-" reads stdin).
In the file, blank lines and # comments are ignored, and an optional second
field names the session:

  https://mirror.example.com/pub/   mirror
  https://bucket.s3.amazonaws.com/
  # https://skipped.example.com/

Sites are indexed one after another; a failure does not stop the rest.`,
		Example: "  dirhop scan -f urls.txt\n  dirhop scan https://a.example/ https://b.example/\n  cat urls.txt | dirhop scan -f - --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validMetadata(metadata); err != nil {
				return err
			}
			specs, err := collectURLs(args, file, opt.name, cmd.InOrStdin())
			if err != nil {
				return err
			}
			return indexURLs(cmd.Context(), a, specs, true, metadata, opt, out)
		}}
	cmd.Flags().StringVarP(&metadata, "metadata", "m", metadata, "minimal, normal (listing metadata), or full (adds one HEAD per file)")
	cmd.Flags().StringVarP(&file, "file", "f", "", "read URLs from FILE, one per line (- for stdin)")
	return cmd
}

func validMetadata(level string) error {
	switch level {
	case "minimal", "normal", "full":
		return nil
	}
	return fmt.Errorf("%w: --metadata must be minimal, normal, or full", ErrInvalidArguments)
}

// enrich runs the optional HEAD pass. minimal and normal use listing data only.
func enrich(ctx context.Context, a *app.App, site *model.Site, level string, opt *options, out io.Writer) error {
	if level != "full" {
		return nil
	}
	if !opt.quiet {
		fmt.Fprintf(out, "Fetching full metadata for %d files (HEAD requests)...\n", site.FileCount)
	}
	n, err := a.EnrichMetadata(ctx, site, 0)
	if err != nil {
		return fmt.Errorf("%w: %v", app.ErrNetwork, err)
	}
	if !opt.quiet {
		fmt.Fprintf(out, "Updated metadata for %d files\n", n)
	}
	return nil
}

func newConfig(a *app.App, out io.Writer) *cobra.Command {
	group := &cobra.Command{Use: "config", Short: "Show configuration information"}
	group.AddCommand(&cobra.Command{Use: "path", Args: cobra.NoArgs, Run: func(*cobra.Command, []string) {
		fmt.Fprintln(out, a.Config.Paths.ConfigFile)
	}})
	return group
}

func selected(ctx context.Context, a *app.App, opt *options) (*model.Site, error) {
	if opt.url != "" {
		site, _, err := a.OpenURL(ctx, opt.url, opt.name, false)
		return site, err
	}
	return a.Site(ctx, opt.session)
}

func newLS(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var long, human, all, reverse bool
	var sortBy string
	cmd := &cobra.Command{Use: "ls [path]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		p := "."
		if len(args) == 1 {
			p = args[0]
		}
		entries, err := a.FS(site).List(cmd.Context(), p)
		if err != nil {
			return mapFSError(err)
		}
		sort.SliceStable(entries, func(i, j int) bool {
			less := false
			switch sortBy {
			case "size":
				less = sizeOf(entries[i]) < sizeOf(entries[j])
			case "date":
				less = timeOf(entries[i]).Before(timeOf(entries[j]))
			default:
				less = strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
			}
			if reverse {
				return !less
			}
			return less
		})
		if opt.json {
			return output.JSON(out, entries)
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		defer w.Flush()
		for _, e := range entries {
			if !all && strings.HasPrefix(e.Name, ".") {
				continue
			}
			name := e.Name
			if e.IsDir() {
				name += "/"
			}
			if !long {
				fmt.Fprintln(w, name)
				continue
			}
			sz := output.Raw(sizeOf(e))
			if human {
				sz = output.Size(sizeOf(e))
			}
			modified := "-"
			if e.ModifiedAt != nil {
				modified = e.ModifiedAt.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", sz, modified, name)
		}
		return nil
	}}
	cmd.Flags().BoolVarP(&long, "long", "l", false, "long listing")
	cmd.Flags().Bool("help", false, "help for ls")
	cmd.Flags().BoolVarP(&human, "human-readable", "h", false, "human-readable sizes")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include hidden entries")
	cmd.Flags().StringVar(&sortBy, "sort", "name", "sort by name, size, or date")
	cmd.Flags().BoolVarP(&reverse, "reverse", "r", false, "reverse sort")
	return cmd
}

func newCD(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "cd [path]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		p := "/"
		if len(args) == 1 {
			p = args[0]
		}
		e, err := a.FS(site).ChangeDirectory(cmd.Context(), p)
		if err != nil {
			return mapFSError(err)
		}
		if !opt.quiet {
			fmt.Fprintln(out, e.NormalizedPath)
		}
		return nil
	}}
}

func newPWD(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "pwd", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		cwd, err := a.FS(site).CWD(cmd.Context())
		if err == nil {
			fmt.Fprintln(out, cwd)
		}
		return err
	}}
}

func newTree(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var depth int
	var dirsOnly, filesOnly, sizes bool
	cmd := &cobra.Command{Use: "tree [path]", Short: "Display the indexed hierarchy in tree(1) format", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := exclusive(cmd, "dirs-only", "files-only"); err != nil {
			return err
		}
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		p := "."
		if len(args) > 0 {
			p = args[0]
		}
		fs := a.FS(site)
		rootEntry, err := fs.Resolve(cmd.Context(), p)
		if err != nil {
			return mapFSError(err)
		}
		var entries []model.Entry
		err = fs.Walk(cmd.Context(), p, func(e model.Entry) error { entries = append(entries, e); return nil })
		if err != nil {
			return mapFSError(err)
		}
		if opt.json {
			return output.JSON(out, entries)
		}
		return printTree(out, p, *rootEntry, entries, treeOptions{
			depth: depth, dirsOnly: dirsOnly, filesOnly: filesOnly, sizes: sizes,
		})
	}}
	cmd.Flags().IntVarP(&depth, "depth", "L", 0, "maximum depth")
	cmd.Flags().BoolVarP(&dirsOnly, "dirs-only", "d", false, "show directories only")
	cmd.Flags().BoolVarP(&filesOnly, "files-only", "f", false, "show files only")
	cmd.Flags().BoolVar(&sizes, "sizes", false, "show exact file sizes in bytes")
	return cmd
}

type treeOptions struct {
	depth, dirs, files int
	dirsOnly           bool
	filesOnly          bool
	sizes              bool
}

type treeNode struct {
	entry    model.Entry
	children []*treeNode
}

// printTree renders the same branch grammar as tree(1): siblings use ├──,
// the final child uses └──, and │ is carried through every non-final ancestor.
func printTree(out io.Writer, requestedPath string, root model.Entry, entries []model.Entry, opt treeOptions) error {
	nodes := make(map[int64]*treeNode, len(entries))
	for _, entry := range entries {
		nodes[entry.ID] = &treeNode{entry: entry}
	}
	rootNode := nodes[root.ID]
	if rootNode == nil {
		rootNode = &treeNode{entry: root}
		nodes[root.ID] = rootNode
	}
	for _, node := range nodes {
		if node.entry.ID == root.ID || node.entry.ParentID == nil {
			continue
		}
		if parent := nodes[*node.entry.ParentID]; parent != nil {
			parent.children = append(parent.children, node)
		}
	}
	var sortNodes func(*treeNode)
	sortNodes = func(node *treeNode) {
		sort.SliceStable(node.children, func(i, j int) bool {
			a, b := node.children[i].entry.Name, node.children[j].entry.Name
			lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
			if lowerA == lowerB {
				return a < b
			}
			return lowerA < lowerB
		})
		for _, child := range node.children {
			sortNodes(child)
		}
	}
	sortNodes(rootNode)

	rootLabel := requestedPath
	if rootLabel == "" {
		rootLabel = "."
	}
	fmt.Fprintln(out, treeEntryLabel(rootNode.entry, rootLabel, opt.sizes))
	if rootNode.entry.IsDir() && !opt.filesOnly {
		opt.dirs++
	} else if rootNode.entry.IsFile() && !opt.dirsOnly {
		opt.files++
	}

	if opt.filesOnly {
		var files []*treeNode
		collectTreeFiles(rootNode, 0, opt.depth, &files)
		for i, node := range files {
			rel := strings.TrimPrefix(strings.TrimPrefix(node.entry.NormalizedPath, root.NormalizedPath), "/")
			connector := "├── "
			if i == len(files)-1 {
				connector = "└── "
			}
			fmt.Fprintln(out, connector+treeEntryLabel(node.entry, rel, opt.sizes))
			opt.files++
		}
	} else {
		renderTreeChildren(out, rootNode, "", 0, &opt)
	}

	fmt.Fprintln(out)
	switch {
	case opt.dirsOnly:
		fmt.Fprintf(out, "%d %s\n", opt.dirs, plural(opt.dirs, "directory", "directories"))
	default:
		fmt.Fprintf(out, "%d %s, %d %s\n", opt.dirs, plural(opt.dirs, "directory", "directories"), opt.files, plural(opt.files, "file", "files"))
	}
	return nil
}

func renderTreeChildren(out io.Writer, parent *treeNode, prefix string, parentDepth int, opt *treeOptions) {
	if opt.depth > 0 && parentDepth >= opt.depth {
		return
	}
	children := parent.children
	if opt.dirsOnly {
		children = slices.DeleteFunc(slices.Clone(children), func(node *treeNode) bool { return !node.entry.IsDir() })
	}
	for i, child := range children {
		last := i == len(children)-1
		connector, continuation := "├── ", "│   "
		if last {
			connector, continuation = "└── ", "    "
		}
		fmt.Fprintln(out, prefix+connector+treeEntryLabel(child.entry, child.entry.Name, opt.sizes))
		if child.entry.IsDir() {
			opt.dirs++
		} else if child.entry.IsFile() {
			opt.files++
		}
		renderTreeChildren(out, child, prefix+continuation, parentDepth+1, opt)
	}
}

func collectTreeFiles(node *treeNode, depth, maxDepth int, files *[]*treeNode) {
	for _, child := range node.children {
		childDepth := depth + 1
		if maxDepth > 0 && childDepth > maxDepth {
			continue
		}
		if child.entry.IsFile() {
			*files = append(*files, child)
		}
		if child.entry.IsDir() {
			collectTreeFiles(child, childDepth, maxDepth, files)
		}
	}
}

func treeEntryLabel(entry model.Entry, name string, sizes bool) string {
	if sizes && entry.IsFile() {
		return fmt.Sprintf("[%11s]  %s", output.Raw(sizeOf(entry)), name)
	}
	return name
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func newStat(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "stat <path>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		e, err := a.FS(site).Stat(cmd.Context(), args[0])
		if err != nil {
			return mapFSError(err)
		}
		if opt.json {
			return output.JSON(out, e)
		}
		fmt.Fprintf(out, "Path:          %s\nType:          %s\nSize:          %s\nModified:      %s\nMIME:          %s\nURL:           %s\nETag:          %s\nLast-Modified: %s\n", e.NormalizedPath, e.Type, output.Size(sizeOf(*e)), formatTime(e.ModifiedAt), e.ContentType, e.URL, e.ETag, e.LastModified)
		return nil
	}}
}

func newDU(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var human bool
	cmd := &cobra.Command{Use: "du [path]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		p := "."
		if len(args) > 0 {
			p = args[0]
		}
		du, err := a.FS(site).DU(cmd.Context(), p)
		if err != nil {
			return mapFSError(err)
		}
		if opt.json {
			return output.JSON(out, du)
		}
		fmt.Fprintf(out, "Directories: %d\nFiles:       %d\nSize:        %s\n", du.Directories, du.Files, output.Size(du.Bytes))
		return nil
	}}
	cmd.Flags().Bool("help", false, "help for du")
	cmd.Flags().BoolVarP(&human, "human-readable", "h", true, "human-readable sizes")
	_ = human
	return cmd
}

func newFind(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var regex, ext, sizeRange, modifiedAfter, typeName string
	cmd := &cobra.Command{Use: "find [pattern]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		find := model.FindOptions{Regex: regex}
		if len(args) > 0 {
			find.Glob = args[0]
			if !strings.ContainsAny(find.Glob, "*?[") {
				find.Glob = "*" + find.Glob + "*"
			}
		}
		if ext != "" {
			find.Extensions = strings.Split(ext, ",")
		}
		find.MinSize, find.MaxSize, err = filesystem.ParseSizeFilter(sizeRange)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
		}
		if modifiedAfter != "" {
			t, e := time.Parse("2006-01-02", modifiedAfter)
			if e != nil {
				return fmt.Errorf("%w: invalid date", ErrInvalidArguments)
			}
			find.ModifiedAfter = &t
		}
		switch typeName {
		case "file":
			find.Type = model.EntryTypeFile
		case "directory", "dir":
			find.Type = model.EntryTypeDirectory
		case "":
		default:
			return fmt.Errorf("%w: invalid type", ErrInvalidArguments)
		}
		entries, err := a.FS(site).Find(cmd.Context(), "/", find)
		if err != nil {
			return mapFSError(err)
		}
		if opt.json {
			return output.JSON(out, entries)
		}
		for _, e := range entries {
			fmt.Fprintln(out, strings.TrimPrefix(e.NormalizedPath, "/"))
		}
		return nil
	}}
	cmd.Flags().StringVarP(&regex, "regex", "r", "", "regular expression")
	cmd.Flags().StringVarP(&ext, "ext", "e", "", "extension or comma-separated extensions")
	cmd.Flags().StringVar(&sizeRange, "size", "", "size constraint, for example >1GB or 100MB..2GB")
	cmd.Flags().StringVar(&modifiedAfter, "modified-after", "", "modified on or after YYYY-MM-DD")
	cmd.Flags().StringVarP(&typeName, "type", "t", "", "file or directory")
	return cmd
}

func newSearch(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "search <text>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		entries, err := a.FS(site).Search(cmd.Context(), "/", args[0], 0)
		if err != nil {
			return mapFSError(err)
		}
		if opt.json {
			return output.JSON(out, entries)
		}
		for _, e := range entries {
			fmt.Fprintln(out, strings.TrimPrefix(e.NormalizedPath, "/"))
		}
		return nil
	}}
}

func newURLs(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var filesOnly, dirsOnly bool
	var ext, include string
	cmd := &cobra.Command{Use: "urls [path]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := exclusive(cmd, "files-only", "dirs-only"); err != nil {
			return err
		}
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		p := "/"
		if len(args) > 0 {
			p = args[0]
		}
		root, err := a.FS(site).Resolve(cmd.Context(), p)
		if err != nil {
			return mapFSError(err)
		}
		var entries []model.Entry
		err = a.FS(site).Walk(cmd.Context(), p, func(e model.Entry) error {
			if e.ID != root.ID {
				entries = append(entries, e)
			}
			return nil
		})
		if err != nil {
			return err
		}
		var urls []string
		for _, e := range entries {
			if filesOnly && !e.IsFile() {
				continue
			}
			if dirsOnly && !e.IsDir() {
				continue
			}
			if !filesOnly && !dirsOnly && !e.IsFile() {
				continue
			}
			if ext != "" && strings.TrimPrefix(strings.ToLower(e.Extension), ".") != strings.TrimPrefix(strings.ToLower(ext), ".") {
				continue
			}
			if include != "" {
				ok, _ := path.Match(include, e.Name)
				if !ok && !strings.Contains(strings.ToLower(e.NormalizedPath), strings.ToLower(include)) {
					continue
				}
			}
			urls = append(urls, e.URL)
		}
		if opt.json {
			return output.JSON(out, urls)
		}
		for _, u := range urls {
			fmt.Fprintln(out, u)
		}
		return nil
	}}
	cmd.Flags().BoolVarP(&filesOnly, "files-only", "f", false, "files only")
	cmd.Flags().BoolVarP(&dirsOnly, "dirs-only", "d", false, "directories only")
	cmd.Flags().StringVarP(&ext, "ext", "e", "", "filter extension")
	cmd.Flags().StringVarP(&include, "include", "i", "", "glob or substring filter")
	return cmd
}

func newDownload(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var destination string
	var workers, segments int
	var resume, overwrite, skip, all bool
	var maxRate, include, exclude string
	cmd := &cobra.Command{Use: "download <path> [path...]", Args: func(_ *cobra.Command, args []string) error {
		if all && len(args) == 0 || !all && len(args) > 0 {
			return nil
		}
		return fmt.Errorf("%w: provide paths or --all", ErrInvalidArguments)
	}, RunE: func(cmd *cobra.Command, args []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		policy := downloader.ExistingError
		if overwrite {
			policy = downloader.ExistingOverwrite
		}
		if skip {
			policy = downloader.ExistingSkip
		}
		rate := int64(0)
		if maxRate != "" {
			rate, err = filesystem.ParseSize(maxRate)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
			}
		}
		if err := exclusive(cmd, "overwrite", "skip-existing"); err != nil {
			return err
		}
		if all {
			args = []string{"/"}
		}
		result, err := a.Download(cmd.Context(), site, args, destination, downloader.Options{Concurrency: workers, Policy: policy, Retries: a.Config.Retries, Segments: segments, MaxRate: rate, Restart: !resume, Include: include, Exclude: exclude})
		if opt.json {
			_ = output.JSON(out, result)
		} else {
			fmt.Fprintf(out, "Completed: %d  Skipped: %d  Failed: %d  Bytes: %s\n", result.Completed, result.Skipped, result.Failed, output.Size(result.Bytes))
		}
		return err
	}}
	cmd.Flags().StringVarP(&destination, "output", "o", a.Config.DownloadDirectory, "destination directory")
	cmd.Flags().IntVarP(&workers, "workers", "w", a.Config.DownloadWorkers, "download workers")
	cmd.Flags().IntVar(&segments, "segments", 1, "segments for large files")
	cmd.Flags().BoolVar(&resume, "resume", true, "resume partial files")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing files")
	cmd.Flags().BoolVar(&skip, "skip-existing", false, "skip existing files")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "download all indexed files")
	cmd.Flags().StringVarP(&include, "include", "i", "", "include matching relative paths")
	cmd.Flags().StringVarP(&exclude, "exclude", "e", "", "exclude matching relative paths")
	cmd.Flags().StringVar(&maxRate, "max-rate", "", "approximate bytes per second, for example 10MB")
	return cmd
}

func newRefresh(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var full bool
	metadata := a.Config.Metadata
	cmd := &cobra.Command{Use: "refresh", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		if err := validMetadata(metadata); err != nil {
			return err
		}
		if err = a.Crawl(cmd.Context(), site, full); err != nil {
			return err
		}
		if err := enrich(cmd.Context(), a, site, metadata, opt, out); err != nil {
			return err
		}
		site, _ = a.DB.SiteByID(cmd.Context(), site.ID)
		if !opt.quiet {
			fmt.Fprintf(out, "Refreshed %s: %d files, %s\n", site.Name, site.FileCount, output.Size(site.TotalSize))
		}
		return nil
	}}
	cmd.Flags().BoolVarP(&full, "full", "f", false, "force complete reconciliation")
	cmd.Flags().StringVarP(&metadata, "metadata", "m", metadata, "minimal, normal, or full (adds one HEAD per file)")
	return cmd
}

func newInfo(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "info", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		if opt.json {
			return output.JSON(out, site)
		}
		fmt.Fprintf(out, "Name:        %s\nURL:         %s\nHost:        %s\nParser:      %s\nWorking dir: %s\nFiles:       %d\nDirectories: %d\nSize:        %s\nStatus:      %s\nLast crawl:  %s\n", site.Name, site.CanonicalURL, site.Hostname, site.ParserType, site.CWD, site.FileCount, site.DirectoryCount, output.Size(site.TotalSize), site.ScanStatus, formatTime(site.LastCrawledAt))
		return nil
	}}
}

func newErrors(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var limit int
	cmd := &cobra.Command{Use: "errors", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		items, err := a.DB.ListCrawlErrors(cmd.Context(), site.ID, limit)
		if err != nil {
			return err
		}
		if opt.json {
			return output.JSON(out, items)
		}
		for _, e := range items {
			fmt.Fprintf(out, "%s\t%s\t%s\n", e.CreatedAt.Format(time.RFC3339), e.Path, e.Message)
		}
		return nil
	}}
	cmd.Flags().IntVarP(&limit, "limit", "l", 100, "maximum errors")
	return cmd
}

func newSessions(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return printSessions(cmd.Context(), a, opt, out) }}
}

func newSession(a *app.App, opt *options, out io.Writer) *cobra.Command {
	group := &cobra.Command{Use: "session", Short: "Manage persistent sessions"}
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return printSessions(cmd.Context(), a, opt, out) }})
	group.AddCommand(&cobra.Command{Use: "use <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := a.Site(cmd.Context(), args[0]); err != nil {
			return err
		}
		s, err := a.Sessions.Use(cmd.Context(), args[0])
		if err == nil {
			fmt.Fprintf(out, "Active session: %s\n", s.Name)
		}
		return err
	}})
	group.AddCommand(&cobra.Command{Use: "info [name]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selector := opt.session
		if len(args) > 0 {
			selector = args[0]
		}
		s, err := a.Site(cmd.Context(), selector)
		if err != nil {
			return err
		}
		if opt.json {
			return output.JSON(out, s)
		}
		fmt.Fprintf(out, "%s\t%s\t%d files\t%s\n", s.Name, s.CanonicalURL, s.FileCount, output.Size(s.TotalSize))
		return nil
	}})
	group.AddCommand(&cobra.Command{Use: "rename <old> <new>", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := a.Site(cmd.Context(), args[0]); err != nil {
			return err
		}
		s, err := a.Sessions.Rename(cmd.Context(), args[0], args[1])
		if err == nil {
			fmt.Fprintf(out, "Renamed session: %s\n", s.Name)
		}
		return err
	}})
	var yes bool
	del := &cobra.Command{Use: "delete <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return fmt.Errorf("%w: session delete requires --yes", ErrInvalidArguments)
		}
		if _, err := a.Site(cmd.Context(), args[0]); err != nil {
			return err
		}
		return a.Sessions.Remove(cmd.Context(), args[0])
	}}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "confirm permanent deletion of the local index")
	group.AddCommand(del)
	var full bool
	refresh := &cobra.Command{Use: "refresh <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.Site(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return a.Crawl(cmd.Context(), s, full)
	}}
	refresh.Flags().BoolVarP(&full, "full", "f", false, "full reconciliation")
	group.AddCommand(refresh)
	return group
}

func printSessions(ctx context.Context, a *app.App, opt *options, out io.Writer) error {
	sites, err := a.Sessions.List(ctx)
	if err != nil {
		return err
	}
	var activeID int64
	if active, e := a.Sessions.Active(ctx); e == nil {
		activeID = active.ID
	}
	if opt.json {
		return output.JSON(out, sites)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "\tNAME\tHOST\tFILES\tSIZE\tLAST SCAN")
	for _, s := range sites {
		mark := " "
		if s.ID == activeID {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", mark, s.Name, s.Hostname, s.FileCount, output.Size(s.TotalSize), formatTime(s.LastCrawledAt))
	}
	return nil
}

// exclusive rejects flags that cannot be combined. Cobra's own flag-group
// check runs before RunE and surfaces as a generic error, which would exit 1;
// doing it here keeps the message style and the exit-2 contract.
func exclusive(cmd *cobra.Command, names ...string) error {
	var set []string
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			set = append(set, "--"+name)
		}
	}
	if len(set) > 1 {
		return fmt.Errorf("%w: %s cannot be used together", ErrInvalidArguments, strings.Join(set, " and "))
	}
	return nil
}

func mapFSError(err error) error {
	if errors.Is(err, filesystem.ErrNotFound) {
		return fmt.Errorf("%w: %v", app.ErrPathNotFound, err)
	}
	return err
}
func sizeOf(e model.Entry) int64 {
	if e.Size == nil {
		return -1
	}
	return *e.Size
}
func timeOf(e model.Entry) time.Time {
	if e.ModifiedAt == nil {
		return time.Time{}
	}
	return *e.ModifiedAt
}
func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
func isHTTPURL(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "http://") || strings.HasPrefix(strings.ToLower(s), "https://")
}

// configArgument finds --config/-c before cobra parses, because the config
// file decides where the index lives and must be read to build the commands.
// It accepts every POSIX spelling: --config F, --config=F, -c F, -c=F, -cF.
func configArgument(args []string) string {
	for i, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c="):
			return strings.TrimPrefix(arg, "-c=")
		case arg == "--config" || arg == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--"):
			return strings.TrimPrefix(arg, "-c")
		}
	}
	return ""
}
