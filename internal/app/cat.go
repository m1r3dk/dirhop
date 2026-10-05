package app

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/m1r3dk/dirhop/internal/filesystem"
	"github.com/m1r3dk/dirhop/internal/model"
)

// Cat streams indexed remote files to out in argument order, exactly like
// cat(1): no headers, separators, synthetic newline, or temporary files.
func (a *App) Cat(ctx context.Context, site *model.Site, paths []string, out io.Writer) error {
	if out == nil {
		return fmt.Errorf("cat output is nil")
	}
	fs := a.FS(site)
	for _, requested := range paths {
		entry, err := fs.Resolve(ctx, requested)
		if err != nil {
			return err
		}
		if !entry.IsFile() {
			return fmt.Errorf("%w: %s", filesystem.ErrNotFile, entry.NormalizedPath)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.URL, nil)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrNetwork, entry.NormalizedPath, err)
		}
		if a.Config.UserAgent != "" {
			request.Header.Set("User-Agent", a.Config.UserAgent)
		}
		response, err := a.HTTP.Do(request)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrNetwork, entry.NormalizedPath, err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			return fmt.Errorf("%w: %s: HTTP %s", ErrNetwork, entry.NormalizedPath, response.Status)
		}
		_, copyErr := io.Copy(out, response.Body)
		closeErr := response.Body.Close()
		if copyErr != nil {
			return fmt.Errorf("cat %s: %w", entry.NormalizedPath, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("cat %s: %w", entry.NormalizedPath, closeErr)
		}
	}
	return nil
}
