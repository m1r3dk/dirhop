package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/m1r3dk/dirclone/internal/app"
	"github.com/m1r3dk/dirclone/internal/config"
	"github.com/m1r3dk/dirclone/internal/downloader"
	"github.com/m1r3dk/dirclone/internal/filesystem"
	"github.com/m1r3dk/dirclone/internal/model"
	"github.com/m1r3dk/dirclone/internal/output"
	"github.com/m1r3dk/dirclone/internal/shell"
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
	var err error
	runShell := func(ctx context.Context, site *model.Site) error {
		return shell.Run(ctx, application, site, os.Stdin, stdout, shellExec(application))
	}

	root := &cobra.Command{
		Use:           "dirclone [URL]",
		Short:         "Persistent remote filesystem for HTTP directory listings",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var site *model.Site
			if len(args) == 1 {
				if !isHTTPURL(args[0]) {
					return fmt.Errorf("%w: expected an HTTP/HTTPS URL", ErrInvalidArguments)
				}
				var created bool
				site, created, err = application.OpenURL(ctx, args[0], opt.name, true)
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
			} else {
				site, err = application.Site(ctx, opt.session)
				if errors.Is(err, app.ErrNoSession) && opt.session == "" {
					site, err = pickSession(ctx, application, os.Stdin, stdout)
				}
				if err != nil {
					return err
				}
			}
			return runShell(ctx, site)
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
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
	flags.StringVar(&opt.config, "config", cfg.Paths.ConfigFile, "configuration file")
	flags.StringVar(&opt.url, "url", "", "select or create a session by URL for this command")
	flags.StringVar(&opt.name, "name", "", "custom name when creating a URL session")
	flags.BoolVar(&opt.json, "json", false, "emit JSON")
	flags.BoolVar(&opt.quiet, "quiet", opt.quiet, "suppress non-essential output")
	flags.BoolVar(&opt.noColor, "no-color", false, "disable color output")
	flags.BoolVarP(&opt.verbose, "verbose", "v", false, "show verbose diagnostics")
	flags.BoolVar(&opt.debug, "debug", false, "show debug diagnostics")

	addCommands(root, application, opt, stdout)
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
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum records")
	return cmd
}

func newScan(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "scan <url>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		site, created, err := a.OpenURL(cmd.Context(), args[0], opt.name, true)
		if err != nil {
			return err
		}
		if !created {
			return a.Crawl(cmd.Context(), site, true)
		}
		if !opt.quiet {
			fmt.Fprintf(out, "Indexed %s: %d files, %s\n", site.Name, site.FileCount, output.Size(site.TotalSize))
		}
		return nil
	}}
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
	site, err := a.Site(ctx, opt.session)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, app.ErrNoSession
	}
	return site, err
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
	cmd.Flags().BoolVar(&reverse, "reverse", false, "reverse sort")
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
	cmd := &cobra.Command{Use: "tree [path]", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		fmt.Fprintln(out, displayName(*rootEntry))
		baseDepth := strings.Count(strings.Trim(rootEntry.NormalizedPath, "/"), "/")
		if rootEntry.NormalizedPath != "/" {
			baseDepth++
		}
		for _, e := range entries {
			if e.ID == rootEntry.ID {
				continue
			}
			relDepth := strings.Count(strings.Trim(e.NormalizedPath, "/"), "/") + 1 - baseDepth
			if depth > 0 && relDepth > depth {
				continue
			}
			if dirsOnly && !e.IsDir() {
				continue
			}
			if filesOnly && !e.IsFile() {
				continue
			}
			name := displayName(e)
			if sizes && e.IsFile() {
				name = "[" + output.Size(sizeOf(e)) + "] " + name
			}
			fmt.Fprintf(out, "%s└── %s\n", strings.Repeat("    ", max(0, relDepth-1)), name)
		}
		return nil
	}}
	cmd.Flags().IntVar(&depth, "depth", 0, "maximum depth")
	cmd.Flags().BoolVar(&dirsOnly, "dirs-only", false, "show directories only")
	cmd.Flags().BoolVar(&filesOnly, "files-only", false, "show files only")
	cmd.Flags().BoolVar(&sizes, "sizes", false, "show file sizes")
	return cmd
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
	cmd.Flags().StringVar(&regex, "regex", "", "regular expression")
	cmd.Flags().StringVar(&ext, "ext", "", "extension or comma-separated extensions")
	cmd.Flags().StringVar(&sizeRange, "size", "", "size constraint, for example >1GB or 100MB..2GB")
	cmd.Flags().StringVar(&modifiedAfter, "modified-after", "", "modified on or after YYYY-MM-DD")
	cmd.Flags().StringVar(&typeName, "type", "", "file or directory")
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
	cmd.Flags().BoolVar(&filesOnly, "files-only", false, "files only")
	cmd.Flags().BoolVar(&dirsOnly, "dirs-only", false, "directories only")
	cmd.MarkFlagsMutuallyExclusive("files-only", "dirs-only")
	cmd.Flags().StringVar(&ext, "ext", "", "filter extension")
	cmd.Flags().StringVar(&include, "include", "", "glob or substring filter")
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
	cmd.Flags().IntVar(&workers, "workers", a.Config.DownloadWorkers, "download workers")
	cmd.Flags().IntVar(&segments, "segments", 1, "segments for large files")
	cmd.Flags().BoolVar(&resume, "resume", true, "resume partial files")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing files")
	cmd.Flags().BoolVar(&skip, "skip-existing", false, "skip existing files")
	cmd.Flags().BoolVar(&all, "all", false, "download all indexed files")
	cmd.Flags().StringVar(&include, "include", "", "include matching relative paths")
	cmd.Flags().StringVar(&exclude, "exclude", "", "exclude matching relative paths")
	cmd.Flags().StringVar(&maxRate, "max-rate", "", "approximate bytes per second, for example 10MB")
	cmd.MarkFlagsMutuallyExclusive("overwrite", "skip-existing")
	return cmd
}

func newRefresh(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var full bool
	cmd := &cobra.Command{Use: "refresh", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		site, err := selected(cmd.Context(), a, opt)
		if err != nil {
			return err
		}
		if err = a.Crawl(cmd.Context(), site, full); err != nil {
			return err
		}
		site, _ = a.DB.SiteByID(cmd.Context(), site.ID)
		if !opt.quiet {
			fmt.Fprintf(out, "Refreshed %s: %d files, %s\n", site.Name, site.FileCount, output.Size(site.TotalSize))
		}
		return nil
	}}
	cmd.Flags().BoolVar(&full, "full", false, "force complete reconciliation")
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
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum errors")
	return cmd
}

func newSessions(a *app.App, opt *options, out io.Writer) *cobra.Command {
	return &cobra.Command{Use: "sessions", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return printSessions(cmd.Context(), a, opt, out) }}
}

func newSession(a *app.App, opt *options, out io.Writer) *cobra.Command {
	group := &cobra.Command{Use: "session", Short: "Manage persistent sessions"}
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return printSessions(cmd.Context(), a, opt, out) }})
	group.AddCommand(&cobra.Command{Use: "use <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
		return a.Sessions.Remove(cmd.Context(), args[0])
	}}
	del.Flags().BoolVar(&yes, "yes", false, "confirm permanent deletion of the local index")
	group.AddCommand(del)
	var full bool
	refresh := &cobra.Command{Use: "refresh <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.Site(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return a.Crawl(cmd.Context(), s, full)
	}}
	refresh.Flags().BoolVar(&full, "full", false, "full reconciliation")
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
func displayName(e model.Entry) string {
	name := e.Name
	if e.NormalizedPath == "/" {
		name = "/"
	}
	if e.IsDir() && name != "/" {
		name += "/"
	}
	return name
}
func isHTTPURL(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "http://") || strings.HasPrefix(strings.ToLower(s), "https://")
}

func configArgument(args []string) string {
	for i, arg := range args {
		if strings.HasPrefix(arg, "--config=") {
			return strings.TrimPrefix(arg, "--config=")
		}
		if arg == "--config" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
