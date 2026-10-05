package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/m1r3dk/dirhop/internal/app"
	"github.com/m1r3dk/dirhop/internal/grayhat"
	"github.com/m1r3dk/dirhop/internal/output"
)

// newGHW builds the `ghw` command: search GrayHatWarfare's public-bucket index
// for buckets (default) or files, then optionally index matched buckets with
// dirhop's own crawler via --scan. The API key comes from config or the
// GRAYHATWARFARE_API_KEY environment variable.
func newGHW(a *app.App, opt *options, out io.Writer) *cobra.Command {
	var (
		files     bool
		typ       string
		ext       string
		limit     int
		order     string
		direction string
		fullPath  bool
		scan      bool
		urlsOnly  bool
	)
	cmd := &cobra.Command{
		Use:   "ghw [keywords]",
		Short: "Search GrayHatWarfare for public buckets and files",
		Long: `Search the GrayHatWarfare public-bucket index for open buckets (default) or
files, then browse the results with dirhop.

Requires an API key from https://grayhatwarfare.com/. Set it in the config as
grayhatwarfare_api_key, or via the GRAYHATWARFARE_API_KEY environment variable.

With --scan, each matched bucket is indexed into a dirhop session so you can
immediately ls/find/download it. GrayHatWarfare covers exactly the bucket types
dirhop browses: AWS S3, Azure Blob, DigitalOcean Spaces, Google Cloud.`,
		Example: `  dirhop ghw backup                     buckets whose name matches "backup"
  dirhop ghw --type azure company       Azure containers matching "company"
  dirhop ghw --files --ext sql,zip dump files named like dump with those types
  dirhop ghw invoices --scan            index every matched bucket into dirhop
  dirhop ghw secrets --urls | dirhop scan -f -   pipe matches into a batch scan`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key := a.Config.GrayHatWarfareAPIKey
			if key == "" {
				return fmt.Errorf("%w: %v", ErrInvalidArguments, grayhat.ErrNoAPIKey)
			}
			if err := validGHWType(typ, files); err != nil {
				return err
			}
			if limit <= 0 {
				limit = 50
			}
			keywords := strings.Join(args, " ")
			client := grayhat.New(a.HTTP, a.Config.GrayHatWarfareBaseURL, key, a.Config.UserAgent)
			ctx := cmd.Context()
			if files {
				return runGHWFiles(ctx, client, fileParams{
					keywords: keywords, types: typ, ext: ext, order: order,
					direction: direction, fullPath: fullPath, limit: limit,
				}, opt, out)
			}
			return runGHWBuckets(ctx, a, client, bucketParams{
				keywords: keywords, typ: typ, order: order, direction: direction,
				limit: limit, scan: scan, urlsOnly: urlsOnly,
			}, opt, out)
		},
	}
	cmd.Flags().BoolVar(&files, "files", false, "search files instead of buckets")
	cmd.Flags().StringVarP(&typ, "type", "t", "", "bucket type: aws, azure, dos, gcp, ali (files accept a comma list)")
	cmd.Flags().StringVarP(&ext, "ext", "e", "", "file extensions, comma-separated (files only)")
	cmd.Flags().IntVarP(&limit, "limit", "l", 50, "maximum results (1-1000)")
	cmd.Flags().StringVar(&order, "order", "", "sort by: buckets fileCount|bucketName, files size|last_modified")
	cmd.Flags().StringVar(&direction, "direction", "", "sort direction: asc or desc")
	cmd.Flags().BoolVar(&fullPath, "full-path", false, "match keywords against the whole file path (files only)")
	cmd.Flags().BoolVar(&scan, "scan", false, "index each matched bucket into a dirhop session")
	cmd.Flags().BoolVar(&urlsOnly, "urls", false, "print only bucket URLs (for piping into `scan -f -`)")
	return cmd
}

func validGHWType(typ string, files bool) error {
	if typ == "" {
		return nil
	}
	valid := map[string]bool{"aws": true, "azure": true, "dos": true, "gcp": true, "ali": true}
	// Files accept a comma-separated list; buckets take a single type.
	parts := []string{typ}
	if files {
		parts = strings.Split(typ, ",")
	}
	for _, p := range parts {
		if !valid[strings.TrimSpace(p)] {
			return fmt.Errorf("%w: --type must be aws, azure, dos, gcp, or ali", ErrInvalidArguments)
		}
	}
	return nil
}

type bucketParams struct {
	keywords, typ, order, direction string
	limit                           int
	scan, urlsOnly                  bool
}

func runGHWBuckets(ctx context.Context, a *app.App, client *grayhat.Client, p bucketParams, opt *options, out io.Writer) error {
	buckets, total, notice, err := client.SearchBuckets(ctx, grayhat.BucketQuery{
		Keywords: p.keywords, Type: p.typ, Order: p.order, Direction: p.direction, Limit: p.limit,
	})
	if err != nil {
		return mapGHWError(err)
	}
	if opt.json && !p.scan {
		return output.JSON(out, buckets)
	}
	if p.urlsOnly && !p.scan {
		for _, b := range buckets {
			fmt.Fprintln(out, grayhat.BucketURL(b.Bucket))
		}
		return nil
	}
	if p.scan {
		specs := make([]urlSpec, 0, len(buckets))
		for _, b := range buckets {
			specs = append(specs, urlSpec{URL: grayhat.BucketURL(b.Bucket)})
		}
		if len(specs) == 0 {
			if !opt.quiet {
				fmt.Fprintln(out, "No buckets matched; nothing to scan.")
			}
			return nil
		}
		return indexURLs(ctx, a, specs, false, a.Config.Metadata, opt, out)
	}
	if len(buckets) == 0 {
		if !opt.quiet {
			fmt.Fprintln(out, "No buckets matched.")
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tFILES\tBUCKET")
	for _, b := range buckets {
		fmt.Fprintf(w, "%s\t%d\t%s\n", b.Type, b.FileCount, grayhat.BucketURL(b.Bucket))
	}
	w.Flush()
	ghwFooter(out, opt, len(buckets), total, notice)
	return nil
}

type fileParams struct {
	keywords, types, ext, order, direction string
	fullPath                               bool
	limit                                  int
}

func runGHWFiles(ctx context.Context, client *grayhat.Client, p fileParams, opt *options, out io.Writer) error {
	results, total, notice, err := client.SearchFiles(ctx, grayhat.FileQuery{
		Keywords: p.keywords, Types: p.types, Extensions: p.ext, Order: p.order,
		Direction: p.direction, FullPath: p.fullPath, Limit: p.limit,
	})
	if err != nil {
		return mapGHWError(err)
	}
	if opt.json {
		return output.JSON(out, results)
	}
	if len(results) == 0 {
		if !opt.quiet {
			fmt.Fprintln(out, "No files matched.")
		}
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tSIZE\tURL")
	for _, f := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Type, output.Size(f.Size), f.URL)
	}
	w.Flush()
	ghwFooter(out, opt, len(results), total, notice)
	return nil
}

func ghwFooter(out io.Writer, opt *options, shown int, total int64, notice string) {
	if opt.quiet {
		return
	}
	fmt.Fprintf(out, "\nShowing %d of %d matches.\n", shown, total)
	if notice != "" {
		fmt.Fprintf(out, "Note: %s\n", notice)
	}
}

func mapGHWError(err error) error {
	// A rejected or missing key is user-fixable: treat it as an argument error
	// so the exit code is 2 rather than a generic failure.
	if err == grayhat.ErrUnauthorized || err == grayhat.ErrNoAPIKey {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return err
}
