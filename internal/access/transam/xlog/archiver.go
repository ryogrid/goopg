package xlog

// WAL archiver — PG's pgarch.c with xlogarchive.c's notify/check helpers.
//
// A completed WAL segment (and a new timeline history file) is announced by
// an empty pg_wal/archive_status/<file>.ready (XLogArchiveNotify). The
// archiver runs archive_command for each .ready file — timeline history
// files first, then segments oldest first (pgarch_readyXlog) — and renames
// it to <file>.done on success (pgarch_archiveDone). A failure is retried
// NUM_ARCHIVE_RETRIES times one second apart, then the archiver waits for
// the next wakeup. Segment removal keeps every segment that is not .done
// (XLogArchiveCheckDone). M0149-0018a.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/goopg/goopg/internal/utils/activity"
)

const (
	archiveStatusDir = "archive_status"
	// numArchiveRetries and the one-second pause are pgarch.c's
	// NUM_ARCHIVE_RETRIES / pg_usleep(1000000L).
	numArchiveRetries = 3
	// pgarchTimeout is PGARCH_AUTOWAKE_INTERVAL: the archiver also wakes
	// on its own once a minute.
	pgarchTimeout = 60 * time.Second
)

// ArchiverConfig wires the archiver to the server.
type ArchiverConfig struct {
	// DataDir is the cluster directory: archive_command runs there, and
	// %p names files relative to it ("pg_wal/<file>"), as PG's archiver
	// does from the postmaster's working directory.
	DataDir string
	// WALDir is pg_wal (the real directory, after any symlink).
	WALDir string
	// Mode returns archive_mode: "off", "on" or "always".
	Mode func() string
	// Command returns archive_command (PGC_SIGHUP: read per pass).
	Command func() string
	// InRecovery reports whether the server is still in recovery; with
	// archive_mode = on the archiver only works once it is not
	// (XLogArchivingActive vs XLogArchivingAlways).
	InRecovery func() bool
	Logger     *slog.Logger
	// OnStart and OnStop bracket the span in which PG's postmaster would
	// have the archiver process running (PgArchStartupAllowed: archive_mode
	// = on outside recovery, or always): the server shows it as a
	// pg_stat_activity row with backend_type 'archiver' meanwhile.
	OnStart func()
	OnStop  func()
}

// Archiver is the pgarch process.
type Archiver struct {
	cfg  ArchiverConfig
	wake chan struct{}
	// warnedUnconfigured makes the "archive_mode enabled, yet archiving is
	// not configured" warning one per misconfiguration, like PG's
	// periodic reminder.
	warnedUnconfigured bool
}

// NewArchiver returns an archiver; Run starts it.
func NewArchiver(cfg ArchiverConfig) *Archiver {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Archiver{cfg: cfg, wake: make(chan struct{}, 1)}
}

// Enabled reports whether archive_mode asks for archiving at all
// (XLogArchivingActive: on or always). Segments are announced and kept
// whenever it does, even while the archiver itself waits out recovery.
func (a *Archiver) Enabled() bool {
	if a == nil || a.cfg.Mode == nil {
		return false
	}
	m := a.cfg.Mode()
	return m == "on" || m == "always"
}

// active reports whether the archiver archives now: archive_mode = always,
// or on and the server is not in recovery.
func (a *Archiver) active() bool {
	switch a.cfg.Mode() {
	case "always":
		return true
	case "on":
		return a.cfg.InRecovery == nil || !a.cfg.InRecovery()
	}
	return false
}

// Notify is XLogArchiveNotify: write <name>.ready and wake the archiver.
// A nil archiver (no server) ignores it.
func (a *Archiver) Notify(name string) {
	if a == nil || !a.Enabled() {
		return
	}
	if err := writeArchiveStatus(a.cfg.WALDir, name, ".ready"); err != nil {
		a.cfg.Logger.Warn(fmt.Sprintf("could not create archive status file \"%s\": %v",
			filepath.Join(archiveStatusDir, name+".ready"), err))
		return
	}
	a.Wakeup()
}

// NotifySegment announces a completed segment. On a primary (or with
// archive_mode = always) that is XLogArchiveNotifySeg's .ready. A standby
// with archive_mode = on does not archive what it received: the walreceiver
// marks the segment .done (XLogArchiveForceDone in XLogWalRcvClose), so it
// can be removed like any archived one.
func (a *Archiver) NotifySegment(tli uint32, segNo uint64, segSize int64) {
	name := XLogFileName(tli, segNo, segSize)
	if a.cfg.Mode() == "on" && a.cfg.InRecovery != nil && a.cfg.InRecovery() {
		if err := writeArchiveStatus(a.cfg.WALDir, name, ".done"); err != nil {
			a.cfg.Logger.Warn(fmt.Sprintf("could not create archive status file \"%s\": %v",
				filepath.Join(archiveStatusDir, name+".done"), err))
		}
		return
	}
	a.Notify(name)
}

// segmentRemovable is XLogArchiveCheckDone: whether a segment may be
// removed. Without archiving, or during recovery with archive_mode = on,
// every segment may; otherwise only an archived (.done) one, and a segment
// with no status file at all gets its .ready back.
func (a *Archiver) segmentRemovable(walDir, name string) bool {
	if !a.Enabled() {
		return true
	}
	if a.cfg.Mode() != "always" && a.cfg.InRecovery != nil && a.cfg.InRecovery() {
		return true
	}
	return archiveCheckDone(walDir, name)
}

// Wakeup is PgArchWakeup.
func (a *Archiver) Wakeup() {
	if a == nil {
		return
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run archives until ctx ends. A final pass on shutdown mirrors the
// archiver's last pgarch_ArchiverCopyLoop before it exits.
func (a *Archiver) Run(ctx context.Context) {
	t := time.NewTicker(pgarchTimeout)
	defer t.Stop()
	running := false
	for {
		// The archiver "process" starts once archiving is allowed — at
		// startup, or when a promotion's history-file notification wakes
		// it — and keeps running until shutdown.
		if !running && a.active() {
			running = true
			if a.cfg.OnStart != nil {
				a.cfg.OnStart()
			}
		}
		a.copyLoop(ctx)
		select {
		case <-ctx.Done():
			a.copyLoop(context.Background())
			// shell_archive_shutdown: the shell archive module's shutdown
			// callback (elog DEBUG1).
			a.cfg.Logger.Debug("archiver process shutting down")
			if running && a.cfg.OnStop != nil {
				a.cfg.OnStop()
			}
			return
		case <-a.wake:
		case <-t.C:
		}
	}
}

// copyLoop is pgarch_ArchiverCopyLoop: archive every ready file, stopping
// at the first one that still fails after the retries.
func (a *Archiver) copyLoop(ctx context.Context) {
	if !a.active() {
		return
	}
	command := ""
	if a.cfg.Command != nil {
		command = a.cfg.Command()
	}
	for {
		name, ok := readyXlog(a.cfg.WALDir)
		if !ok {
			return
		}
		if command == "" {
			// pgarch_ArchiverCopyLoop: archive_command is not set.
			if !a.warnedUnconfigured {
				a.cfg.Logger.Warn("archive_mode enabled, yet archiving is not configured",
					"hint", `Either "archive_command" or "archive_library" must be set.`)
				a.warnedUnconfigured = true
			}
			return
		}
		a.warnedUnconfigured = false
		failures := 0
		for {
			// A file whose WAL is gone can never be archived: drop its
			// status file and go on (pgarch_ArchiverCopyLoop).
			if _, err := os.Stat(filepath.Join(a.cfg.WALDir, name)); errors.Is(err, os.ErrNotExist) {
				_ = os.Remove(filepath.Join(a.cfg.WALDir, archiveStatusDir, name+".ready"))
				a.cfg.Logger.Warn(fmt.Sprintf("removed orphan archive status file \"%s\"",
					filepath.Join("pg_wal", archiveStatusDir, name+".ready")))
				break
			}
			if a.archiveFile(ctx, command, name) {
				if err := os.Rename(filepath.Join(a.cfg.WALDir, archiveStatusDir, name+".ready"),
					filepath.Join(a.cfg.WALDir, archiveStatusDir, name+".done")); err != nil {
					a.cfg.Logger.Warn(fmt.Sprintf("could not rename archive status file for \"%s\": %v", name, err))
				}
				activity.ReportArchived(name, time.Now())
				break
			}
			activity.ReportArchiveFailed(name, time.Now())
			failures++
			if failures >= numArchiveRetries {
				a.cfg.Logger.Warn(fmt.Sprintf("archiving write-ahead log file \"%s\" failed too many times, will try again later", name))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// archiveFile is shell_archive_file: run archive_command with %p, %f and
// %% replaced, reporting success. The failure messages are PG's.
func (a *Archiver) archiveFile(ctx context.Context, command, name string) bool {
	path := filepath.Join("pg_wal", name)
	xlogCmd := replaceArchivePlaceholders(command, path, name)
	c := exec.CommandContext(ctx, "sh", "-c", xlogCmd)
	c.Dir = a.cfg.DataDir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	err := c.Run()
	if err == nil {
		a.cfg.Logger.Debug(fmt.Sprintf("archived write-ahead log file \"%s\"", name))
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			a.cfg.Logger.Warn(fmt.Sprintf("archive command was terminated by signal %d: %s", ws.Signal(), ws.Signal()),
				"detail", "The failed archive command was: "+xlogCmd)
			return false
		}
		a.cfg.Logger.Warn(fmt.Sprintf("archive command failed with exit code %d", ee.ExitCode()),
			"detail", "The failed archive command was: "+xlogCmd)
		return false
	}
	a.cfg.Logger.Warn(fmt.Sprintf("archive command failed: %v", err),
		"detail", "The failed archive command was: "+xlogCmd)
	return false
}

// replaceArchivePlaceholders expands archive_command's %p (path), %f (file
// name) and %% (a literal %); any other %-sequence is left as written
// (replace_percent_placeholders keeps unknown escapes for archive_command).
func replaceArchivePlaceholders(command, path, name string) string {
	var b strings.Builder
	for i := 0; i < len(command); i++ {
		c := command[i]
		if c != '%' || i+1 >= len(command) {
			b.WriteByte(c)
			continue
		}
		switch command[i+1] {
		case 'p':
			b.WriteString(path)
			i++
		case 'f':
			b.WriteString(name)
			i++
		case '%':
			b.WriteByte('%')
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// readyXlog is pgarch_readyXlog: the next file to archive. Timeline
// history files go first — a standby fetching a new timeline needs them
// before its segments — then the oldest segment.
func readyXlog(walDir string) (string, bool) {
	ents, err := os.ReadDir(filepath.Join(walDir, archiveStatusDir))
	if err != nil {
		return "", false
	}
	var ready []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".ready") {
			ready = append(ready, strings.TrimSuffix(n, ".ready"))
		}
	}
	if len(ready) == 0 {
		return "", false
	}
	sort.Slice(ready, func(i, j int) bool {
		hi, hj := strings.HasSuffix(ready[i], ".history"), strings.HasSuffix(ready[j], ".history")
		if hi != hj {
			return hi
		}
		return ready[i] < ready[j]
	})
	return ready[0], true
}

// writeArchiveStatus creates pg_wal/archive_status/<name><suffix>.
func writeArchiveStatus(walDir, name, suffix string) error {
	dir := filepath.Join(walDir, archiveStatusDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name+suffix), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// archiveCheckDone is XLogArchiveCheckDone for a segment about to be
// removed: true when its .done file exists (archived). Without either
// status file the .ready is (re)created so the segment is archived later,
// and the segment is kept.
func archiveCheckDone(walDir, name string) bool {
	dir := filepath.Join(walDir, archiveStatusDir)
	if _, err := os.Stat(filepath.Join(dir, name+".done")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, name+".ready")); err == nil {
		return false
	}
	_ = writeArchiveStatus(walDir, name, ".ready")
	return false
}

// XLogArchiveIsBusy is XLogArchiveIsBusy (xlogarchive.c): whether name in
// walDir still waits to be archived. A .done file, or a file no longer in
// pg_wal (archived and recycled), means it is not busy.
func XLogArchiveIsBusy(walDir, name string) bool {
	dir := filepath.Join(walDir, archiveStatusDir)
	if _, err := os.Stat(filepath.Join(dir, name+".done")); err == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, name+".ready")); err == nil {
		return true
	}
	// Race condition: the archiver may have just finished.
	if _, err := os.Stat(filepath.Join(dir, name+".done")); err == nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(walDir, name)); err != nil {
		return false
	}
	return true
}

// archiveCleanup is XLogArchiveCleanup: drop a removed segment's status
// files.
func archiveCleanup(walDir, name string) {
	dir := filepath.Join(walDir, archiveStatusDir)
	_ = os.Remove(filepath.Join(dir, name+".done"))
	_ = os.Remove(filepath.Join(dir, name+".ready"))
}
