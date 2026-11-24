package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/utils/misc"
)

// Standby-mode archive restore — the XLOG_FROM_ARCHIVE source of PG's
// WaitForWALToBecomeAvailable (xlogrecovery.c). A standby with
// restore_command and no primary_conninfo fetches the segment that holds
// its next WAL byte from the archive (RestoreArchivedFile into
// RECOVERYXLOG), appends the part it does not have yet to its local WAL —
// the same writer the walreceiver feeds, so the continuous replayer reads
// it — and moves on to the next segment. When the archive does not have the
// segment yet, it waits wal_retrieve_retry_interval and tries again.
// M0149-0018b.

// runArchiveFeeder owns done (the receiver's slot in the standby
// controller) and returns when ctx ends: promotion or shutdown. On the way
// out it takes what the archive still has, as PG's startup process reads
// all available WAL before it finishes recovery.
func runArchiveFeeder(ctx context.Context, done chan struct{}, rt *initdb.Runtime, registry *misc.Registry, logger *slog.Logger) {
	defer close(done)
	logger.Info("standby mode: restoring WAL from the archive", "restore_command", stringGUC(registry, "restore_command", ""))
	for {
		if err := feedFromArchive(ctx, rt, registry, logger); err != nil {
			logger.Error(err.Error())
			return
		}
		interval := time.Duration(intGUC(registry, "wal_retrieve_retry_interval", 5000)) * time.Millisecond
		select {
		case <-ctx.Done():
			if err := feedFromArchive(context.Background(), rt, registry, logger); err != nil {
				logger.Error(err.Error())
			}
			return
		case <-time.After(interval):
		}
	}
}

// feedFromArchive restores segments until the archive has no more. It
// returns an error only for a FATAL restore failure (a signal or an
// unrunnable command, or a file of the wrong size); "not in the archive" is
// the normal end of a round.
func feedFromArchive(ctx context.Context, rt *initdb.Runtime, registry *misc.Registry, logger *slog.Logger) error {
	if rt == nil || rt.WAL == nil {
		return nil
	}
	walDir := walDirFor(rt)
	segSize := rt.WAL.SegmentSize()
	for {
		command := stringGUC(registry, "restore_command", "")
		if command == "" || ctx.Err() != nil {
			return nil
		}
		written := rt.WAL.WrittenLSN()
		segNo := written / uint64(segSize)
		off := written % uint64(segSize)
		name := xlog.XLogFileName(rt.WAL.TimelineID(), segNo, segSize)
		err := xlog.RestoreArchivedFileTemp(ctx, command, walDir, name, xlog.RecoveryXLogName, lastRestartPointFileName(rt, segSize))
		if err != nil {
			var fatal *xlog.RestoreFatalError
			if errors.As(err, &fatal) {
				return err
			}
			return nil // not archived yet (or shutting down)
		}
		path := filepath.Join(walDir, xlog.RecoveryXLogName)
		data, rerr := os.ReadFile(path)
		_ = os.Remove(path)
		if rerr != nil {
			return fmt.Errorf("could not read file \"%s\": %w", path, rerr)
		}
		if int64(len(data)) != segSize {
			return fmt.Errorf("archive file \"%s\" has wrong size: %d instead of %d", name, len(data), segSize)
		}
		if off < uint64(len(data)) {
			if _, _, err := rt.WAL.AppendRaw(data[off:]); err != nil {
				return fmt.Errorf("could not write restored WAL file \"%s\": %w", name, err)
			}
		}
	}
}

// lastRestartPointFileName is restore_command's %r: the segment holding the
// last restartpoint's redo (pg_control's checkPointCopy.redo).
func lastRestartPointFileName(rt *initdb.Runtime, segSize int64) string {
	cd, err := control.ReadControlFile(rt.DataDir)
	if err != nil || cd == nil {
		return xlog.XLogFileName(1, 0, segSize)
	}
	tli := cd.CheckPointCopyThisTLI
	if tli == 0 {
		tli = 1
	}
	return xlog.XLogFileName(tli, cd.CheckPointCopyRedo/uint64(segSize), segSize)
}
