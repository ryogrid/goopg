package autovacuum

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// TestLauncherStartStop verifies the launcher runs and stops cleanly.
func TestLauncherStartStop(t *testing.T) {
	cat := catalog.NewInMemory()
	l := NewLauncher(nil, nil, cat)
	l.NapInterval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.Run(ctx)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("Run: %v", err)
	}
}
