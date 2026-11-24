package activity

import (
	"slices"
	"strconv"
	"sync"
)

// BASE_BACKUP command progress — the row source of pg_stat_progress_basebackup
// (pgstat_progress_start_command(PROGRESS_COMMAND_BASEBACKUP) and the
// basebackup_progress_* updates in backup/basebackup_progress.c). One entry
// per walsender currently running BASE_BACKUP, keyed by its backend pid.
// M0149-0077l.

// BaseBackup phases (progress.h PROGRESS_BASEBACKUP_PHASE_*), rendered by the
// view's CASE in system_views.sql.
const (
	BaseBackupPhaseInitializing       = 0
	BaseBackupPhaseWaitCheckpoint     = 1
	BaseBackupPhaseEstimateBackupSize = 2
	BaseBackupPhaseStreamBackup       = 3
	BaseBackupPhaseWaitWALArchive     = 4
	BaseBackupPhaseTransferWAL        = 5
)

var baseBackupPhaseNames = [...]string{
	"initializing",
	"waiting for checkpoint to finish",
	"estimating backup size",
	"streaming database files",
	"waiting for wal archiving to finish",
	"transferring wal files",
}

// BaseBackupProgress is one walsender's BASE_BACKUP progress slot. Methods
// are safe for concurrent use with the view reader.
type BaseBackupProgress struct {
	pid int32

	mu                  sync.Mutex
	phase               int
	backupTotal         int64 // -1 = unknown (the view shows NULL)
	backupStreamed      int64
	tablespacesTotal    int64
	tablespacesStreamed int64
}

var baseBackupProgress sync.Map // int32 pid → *BaseBackupProgress

// StartBaseBackupProgress registers pid's BASE_BACKUP (phase initializing,
// total unknown) and returns its slot; End removes it.
func StartBaseBackupProgress(pid int32) *BaseBackupProgress {
	p := &BaseBackupProgress{pid: pid, backupTotal: -1}
	baseBackupProgress.Store(pid, p)
	return p
}

// End removes the slot (pgstat_progress_end_command).
func (p *BaseBackupProgress) End() {
	if p == nil {
		return
	}
	baseBackupProgress.CompareAndDelete(p.pid, p)
}

// SetPhase records the current phase.
func (p *BaseBackupProgress) SetPhase(phase int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
}

// SetTablespacesTotal records how many archives the backup will send.
func (p *BaseBackupProgress) SetTablespacesTotal(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.tablespacesTotal = n
	p.mu.Unlock()
}

// AddStreamed adds n archive bytes sent.
func (p *BaseBackupProgress) AddStreamed(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.backupStreamed += n
	p.mu.Unlock()
}

// TablespaceDone counts one finished archive.
func (p *BaseBackupProgress) TablespaceDone() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.tablespacesStreamed++
	p.mu.Unlock()
}

// BaseBackupProgressRows renders pg_stat_progress_basebackup's rows (pid,
// phase, backup_total, backup_streamed, tablespaces_total,
// tablespaces_streamed) ordered by pid. null stands in for SQL NULL
// (backup_total is NULL while the size is unknown).
func BaseBackupProgressRows(null string) [][]string {
	var rows [][]string
	var pids []int32
	byPID := map[int32]*BaseBackupProgress{}
	baseBackupProgress.Range(func(k, v any) bool {
		pid := k.(int32)
		pids = append(pids, pid)
		byPID[pid] = v.(*BaseBackupProgress)
		return true
	})
	slices.Sort(pids)
	for _, pid := range pids {
		p := byPID[pid]
		p.mu.Lock()
		phase := ""
		if p.phase >= 0 && p.phase < len(baseBackupPhaseNames) {
			phase = baseBackupPhaseNames[p.phase]
		}
		total := null
		if p.backupTotal >= 0 {
			total = strconv.FormatInt(p.backupTotal, 10)
		}
		rows = append(rows, []string{
			strconv.FormatInt(int64(pid), 10), phase, total,
			strconv.FormatInt(p.backupStreamed, 10),
			strconv.FormatInt(p.tablespacesTotal, 10),
			strconv.FormatInt(p.tablespacesStreamed, 10),
		})
		p.mu.Unlock()
	}
	return rows
}
