package model

import "time"

// EntryType identifies a virtual filesystem node.
type EntryType string

const (
	EntryTypeDirectory EntryType = "directory"
	EntryTypeFile      EntryType = "file"
	EntryTypeUnknown   EntryType = "unknown"
)

// ScanStatus describes the most recent site crawl state.
type ScanStatus string

const (
	ScanStatusPending   ScanStatus = "pending"
	ScanStatusRunning   ScanStatus = "running"
	ScanStatusComplete  ScanStatus = "complete"
	ScanStatusFailed    ScanStatus = "failed"
	ScanStatusCancelled ScanStatus = "cancelled"
)

// CrawlMode controls reconciliation behavior.
type CrawlMode string

const (
	CrawlModeNormal      CrawlMode = "normal"
	CrawlModeIncremental CrawlMode = "incremental"
	CrawlModeFull        CrawlMode = "full"
)

// DownloadStatus describes persisted download state.
type DownloadStatus string

const (
	DownloadPending   DownloadStatus = "pending"
	DownloadRunning   DownloadStatus = "running"
	DownloadComplete  DownloadStatus = "complete"
	DownloadFailed    DownloadStatus = "failed"
	DownloadCancelled DownloadStatus = "cancelled"
)

// Site is a persistent remote directory-listing session.
type Site struct {
	ID               int64
	Name             string
	OriginalURL      string
	CanonicalURL     string
	Hostname         string
	ParserType       string
	CWD              string
	EntryCount       int64
	FileCount        int64
	DirectoryCount   int64
	TotalSize        int64
	ScanStatus       ScanStatus
	CrawlConcurrency int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastCrawledAt    *time.Time
}

// Entry is one indexed virtual filesystem node.
type Entry struct {
	ID             int64
	SiteID         int64
	ParentID       *int64
	Name           string
	NormalizedPath string
	URL            string
	Type           EntryType
	Size           *int64
	ModifiedAt     *time.Time
	ETag           string
	LastModified   string
	ContentType    string
	Extension      string
	Removed        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSeenAt     time.Time
}

func (e Entry) IsDir() bool  { return e.Type == EntryTypeDirectory }
func (e Entry) IsFile() bool { return e.Type == EntryTypeFile }

// CrawlRun records one crawl attempt.
type CrawlRun struct {
	ID            int64
	SiteID        int64
	Mode          CrawlMode
	Status        ScanStatus
	StartedAt     time.Time
	FinishedAt    *time.Time
	Directories   int64
	Files         int64
	Bytes         int64
	ErrorCount    int64
	FailureReason string
	// TargetName and CurrentPath are transient live-progress fields. They are not
	// persisted in crawl_runs; they let terminals show what is being crawled now.
	TargetName  string
	CurrentPath string
}

// CrawlError records a recoverable or terminal crawl error.
type CrawlError struct {
	ID        int64
	RunID     int64
	SiteID    int64
	URL       string
	Path      string
	Operation string
	Message   string
	CreatedAt time.Time
}

// Download records durable download progress.
type Download struct {
	ID          int64
	SiteID      int64
	EntryID     int64
	SourceURL   string
	Destination string
	Status      DownloadStatus
	BytesDone   int64
	TotalBytes  *int64
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// DiskUsage is an aggregate over a path subtree.
type DiskUsage struct {
	Files       int64
	Directories int64
	Bytes       int64
}

// FindOptions filters filesystem entries. Zero values mean no constraint.
type FindOptions struct {
	Glob           string
	Regex          string
	Extensions     []string
	MinSize        *int64
	MaxSize        *int64
	ModifiedAfter  *time.Time
	ModifiedBefore *time.Time
	Type           EntryType
	IncludeRemoved bool
	Limit          int
}
