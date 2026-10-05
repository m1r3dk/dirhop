package app

import (
	"context"
	"time"

	"github.com/m1r3dk/dirhop/internal/database"
	"github.com/m1r3dk/dirhop/internal/downloader"
	"github.com/m1r3dk/dirhop/internal/model"
)

// downloadState records each file's download lifecycle in the downloads table
// so interrupted transfers are visible after restart.
type downloadState struct {
	db     *database.DB
	siteID int64
}

func (s *downloadState) Record(ctx context.Context, e downloader.Event) error {
	if e.Entry.ID == 0 {
		return nil
	}
	row := &model.Download{
		SiteID: s.siteID, EntryID: e.Entry.ID, SourceURL: e.Entry.URL,
		Destination: e.OutputPath, BytesDone: e.Bytes,
	}
	if e.Entry.Size >= 0 {
		size := e.Entry.Size
		row.TotalBytes = &size
	}
	switch e.Status {
	case downloader.StatusPlanned:
		row.Status = model.DownloadPending
	case downloader.StatusRunning:
		row.Status = model.DownloadRunning
	case downloader.StatusCompleted, downloader.StatusSkipped:
		row.Status = model.DownloadComplete
		now := time.Now().UTC()
		row.CompletedAt = &now
	case downloader.StatusFailed:
		row.Status = model.DownloadFailed
		if e.Err != nil {
			row.Error = e.Err.Error()
		}
	}
	return s.db.UpsertDownload(ctx, row)
}
