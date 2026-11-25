package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFormatXminInvalidIsNull: pg_replication_slots shows xmin /
// catalog_xmin as NULL for InvalidTransactionId, as
// pg_get_replication_slots does (001_stream_rep waits for
// `xmin IS NULL AND catalog_xmin IS NULL` on a slot without
// hot_standby_feedback). M0149-0077g.
func TestFormatXminInvalidIsNull(t *testing.T) {
	if got := formatXmin(0); got != catalog.VirtualNull {
		t.Fatalf("formatXmin(0) = %q, want NULL", got)
	}
	if got := formatXmin(742); got != "742" {
		t.Fatalf("formatXmin(742) = %q", got)
	}
}
