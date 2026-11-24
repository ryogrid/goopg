package activity

import (
	"strconv"
	"sync"
	"time"
)

// WAL archiver statistics — pg_stat_archiver's single row
// (pgstat_archiver.c: PgStat_ArchiverStats). The archiver
// (xlog.Archiver) reports each archived or failed file here. M0149-0018a.

var archiverStats struct {
	mu             sync.Mutex
	archivedCount  int64
	lastArchived   string
	lastArchivedAt time.Time
	failedCount    int64
	lastFailed     string
	lastFailedAt   time.Time
	statsReset     time.Time
}

// ReportArchived is pgstat_report_archiver(xlog, false).
func ReportArchived(file string, at time.Time) {
	archiverStats.mu.Lock()
	archiverStats.archivedCount++
	archiverStats.lastArchived = file
	archiverStats.lastArchivedAt = at
	archiverStats.mu.Unlock()
}

// ReportArchiveFailed is pgstat_report_archiver(xlog, true).
func ReportArchiveFailed(file string, at time.Time) {
	archiverStats.mu.Lock()
	archiverStats.failedCount++
	archiverStats.lastFailed = file
	archiverStats.lastFailedAt = at
	archiverStats.mu.Unlock()
}

// ArchiverStatsRow renders pg_stat_archiver (archived_count,
// last_archived_wal, last_archived_time, failed_count, last_failed_wal,
// last_failed_time, stats_reset); null stands in for SQL NULL and
// defaultReset is the stats_reset shown before any reset.
func ArchiverStatsRow(null, defaultReset string) []string {
	s := &archiverStats
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := func(t time.Time) string {
		if t.IsZero() {
			return null
		}
		return t.Format("2006-01-02 15:04:05.999999-07")
	}
	str := func(v string) string {
		if v == "" {
			return null
		}
		return v
	}
	reset := defaultReset
	if !s.statsReset.IsZero() {
		reset = ts(s.statsReset)
	}
	return []string{
		strconv.FormatInt(s.archivedCount, 10), str(s.lastArchived), ts(s.lastArchivedAt),
		strconv.FormatInt(s.failedCount, 10), str(s.lastFailed), ts(s.lastFailedAt), reset,
	}
}

// ResetArchiverStats is pg_stat_reset_shared('archiver'): every counter and
// last_* column cleared, stats_reset set to at.
func ResetArchiverStats(at time.Time) {
	archiverStats.mu.Lock()
	archiverStats.archivedCount, archiverStats.failedCount = 0, 0
	archiverStats.lastArchived, archiverStats.lastFailed = "", ""
	archiverStats.lastArchivedAt, archiverStats.lastFailedAt = time.Time{}, time.Time{}
	archiverStats.statsReset = at
	archiverStats.mu.Unlock()
}
