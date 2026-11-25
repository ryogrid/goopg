package activity

import (
	"testing"
	"time"
)

// TestArchiverStatsReset pins pg_stat_archiver's counters and
// pg_stat_reset_shared('archiver') (M0149-0018a, M0149-0019a).
func TestArchiverStatsReset(t *testing.T) {
	ResetArchiverStats(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	ReportArchiveFailed("000000010000000000000001", time.Now())
	ReportArchived("000000010000000000000001", time.Now())
	row := ArchiverStatsRow("NULL", "boot")
	if row[0] != "1" || row[1] != "000000010000000000000001" || row[3] != "1" || row[4] != "000000010000000000000001" {
		t.Fatalf("row after archive = %v", row)
	}
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	ResetArchiverStats(at)
	row = ArchiverStatsRow("NULL", "boot")
	if row[0] != "0" || row[1] != "NULL" || row[2] != "NULL" || row[3] != "0" || row[4] != "NULL" || row[5] != "NULL" {
		t.Fatalf("row after reset = %v", row)
	}
	if row[6] != at.Format("2006-01-02 15:04:05.999999-07") {
		t.Fatalf("stats_reset = %q", row[6])
	}
}
