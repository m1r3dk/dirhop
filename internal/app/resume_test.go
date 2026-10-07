package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/m1r3dk/dirhop/internal/bucket"
	"github.com/m1r3dk/dirhop/internal/model"
)

// An interrupted bucket scan must keep everything already indexed and, on the
// next run, resume strictly after the last checkpointed key instead of
// re-listing the bucket from the beginning.
func TestBucketScanResumesAfterInterruption(t *testing.T) {
	// The server pages one key per request. The first scan's continuation request
	// is cancelled to simulate a Ctrl-C/crash after the first page committed. The
	// resuming scan must send start-after=a.txt rather than listing from the start.
	cancelCtx, cancel := context.WithCancel(context.Background())
	interrupted := false
	resumeStartAfter := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		q := r.URL.Query()
		after := q.Get("start-after")
		if token := q.Get("continuation-token"); token != "" {
			after = token
		}
		switch {
		case after == "":
			// First page: commit a.txt, signal more pages follow.
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>a.txt</NextContinuationToken>`+
				`<Contents><Key>a.txt</Key><Size>1</Size></Contents></ListBucketResult>`)
		case !interrupted:
			// First scan's continuation: interrupt here, after a.txt is durable.
			interrupted = true
			cancel()
			<-r.Context().Done()
		default:
			// Resuming scan continues after a.txt and finishes with b.txt.
			resumeStartAfter = after
			fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`+
				`<Contents><Key>b.txt</Key><Size>2</Size></Contents></ListBucketResult>`)
		}
	}))
	defer server.Close()

	a := newTestApp(t)
	ctx := context.Background()

	site, _, err := a.Sessions.Open(ctx, server.URL+"/", "resume", true)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse(server.URL + "/")
	target := bucket.Target{Provider: bucket.S3, Endpoint: endpoint}

	// First (interrupted) scan.
	run := &model.CrawlRun{SiteID: site.ID, Mode: model.CrawlModeFull, Status: model.ScanStatusRunning, StartedAt: time.Now().UTC()}
	if err := a.DB.StartCrawlRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	firstStart := run.StartedAt
	if err := a.crawlBucket(cancelCtx, site, target, run); err == nil {
		t.Fatalf("expected the interrupted scan to return an error")
	}

	// a.txt was committed before the interruption and must still be indexed.
	if _, err := a.DB.EntryByPath(ctx, site.ID, "/a.txt", false); err != nil {
		t.Fatalf("interrupted scan lost the already-indexed key a.txt: %v", err)
	}
	cursor, startedAt, ok, err := a.DB.ScanCheckpoint(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || cursor != "a.txt" {
		t.Fatalf("checkpoint cursor=%q ok=%v, want a.txt", cursor, ok)
	}
	if startedAt.Unix() != firstStart.Unix() {
		t.Fatalf("checkpoint started_at=%v, want the original scan start %v", startedAt, firstStart)
	}

	// Resuming scan: fresh context. It must continue after a.txt.
	resumeRun := &model.CrawlRun{SiteID: site.ID, Mode: model.CrawlModeFull, Status: model.ScanStatusRunning, StartedAt: time.Now().UTC()}
	if err := a.DB.StartCrawlRun(ctx, resumeRun); err != nil {
		t.Fatal(err)
	}
	if err := a.crawlBucket(ctx, site, target, resumeRun); err != nil {
		t.Fatalf("resumed scan failed: %v", err)
	}
	if resumeStartAfter != "a.txt" {
		t.Fatalf("resume sent start-after=%q, want a.txt; it re-listed the bucket from the start", resumeStartAfter)
	}
	// The resumed run must keep the original logical start time so later
	// reconciliation does not reap a.txt as stale.
	if resumeRun.StartedAt.Unix() != firstStart.Unix() {
		t.Fatalf("resumed run start=%v, want original %v", resumeRun.StartedAt, firstStart)
	}

	for _, p := range []string{"/a.txt", "/b.txt"} {
		if _, err := a.DB.EntryByPath(ctx, site.ID, p, false); err != nil {
			t.Fatalf("expected %s indexed after resume: %v", p, err)
		}
	}

	// A completed scan clears its checkpoint so the next scan starts fresh.
	if _, _, ok, err := a.DB.ScanCheckpoint(ctx, site.ID); err != nil || ok {
		t.Fatalf("checkpoint not cleared after completion: ok=%v err=%v", ok, err)
	}
}
