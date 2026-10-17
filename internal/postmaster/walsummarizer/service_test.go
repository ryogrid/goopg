package walsummarizer

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestServiceWarnsWithoutFullPageWrites: the summarizer warns when it starts
// with full_page_writes off, because pages goopg dirties without a WAL block
// reference reach a summary only through their full-page image (design §4.4
// guard), and stays quiet when it is on.
func TestServiceWarnsWithoutFullPageWrites(t *testing.T) {
	for _, fpw := range []bool{false, true} {
		var buf bytes.Buffer
		svc := NewService(ServiceConfig{
			DataDir:        t.TempDir(),
			Logger:         slog.New(slog.NewTextHandler(&buf, nil)),
			RetryDelay:     time.Hour,
			FullPageWrites: func() bool { return fpw },
		})
		ctx, cancel := context.WithCancel(context.Background())
		svc.Reconcile(ctx, true)
		svc.Stop()
		cancel()
		warned := strings.Contains(buf.String(), "walsummarizer_fpw_off")
		if warned == fpw {
			t.Errorf("full_page_writes=%v: warned=%v, log:\n%s", fpw, warned, buf.String())
		}
	}
}
