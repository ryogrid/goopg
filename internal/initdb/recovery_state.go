package initdb

import (
	"errors"
	"log/slog"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// startupRecoveryDecision is what Open learns from pg_control before it
// replays anything: whether this start is a crash recovery, and where redo
// must begin. M0131-S20.1/.2.
//
// Until this slice goopg never read pg_control.State at all — the constants
// existed and the field was decoded, but no caller anywhere in internal/ or
// cmd/ looked at it. Startup replayed unconditionally and found its own redo
// start by SCANNING the WAL for the last record its isCheckpointRecord
// happened to recognise. Both halves are replaced here by what upstream
// actually does in InitWalRecovery/PerformWalRecovery.
type startupRecoveryDecision struct {
	// crashRecovery is true when the previous run did not shut down
	// cleanly, i.e. upstream would have set InRecovery = true.
	crashRecovery bool
	// redoLSN is pg_control's checkPointCopy.redo (0-based XLogRecPtr), or
	// 0 when there is no usable control file — see wal.ReplayRecordsFrom
	// for what 0 means to the replay path.
	redoLSN uint64
	// prevState is the DBState pg_control carried on entry, kept for the
	// log line and for the post-replay stamp's before/after reporting.
	prevState uint32
	// nextMulti is pg_control's checkPointCopy.nextMulti — the MultiXactId
	// the previous run would have handed out next. 0 when there is no
	// usable control file; multixact.NewStoreAt clamps anything below
	// FirstMultiXactId, so 0 degrades to the pre-M0131-S20.4 behaviour of
	// starting the allocator at FirstMultiXactId. M0131-S20.4.
	nextMulti uint32
}

// beginRecovery reads pg_control, decides whether this start is a crash
// recovery, and — if it is — durably stamps DB_IN_CRASH_RECOVERY before a
// single record is replayed. Mirrors the InRecovery decision at
// postgres/src/backend/access/transam/xlogrecovery.c:919-961.
//
// Two arms of upstream's decision are implemented; the third
// (ArchiveRecoveryRequested, i.e. a recovery.signal file) belongs to
// wal.RunArchiveRecovery, which runs its own path and stamps
// DB_IN_ARCHIVE_RECOVERY there.
//
//  1. state != DB_SHUTDOWNED ⇒ recovery. Note this is upstream's exact
//     test: DB_SHUTDOWNED_IN_RECOVERY does NOT count as clean, because a
//     standby shut down cleanly still has to replay its way back to a
//     consistent point.
//  2. redo < the checkpoint record's own location ⇒ recovery, because that
//     is the signature of an ONLINE checkpoint, and a cluster whose last
//     checkpoint was online did not shut down through one. goopg's own
//     shutdown checkpoints satisfy redo == CheckPoint (the redo pointer is
//     sampled at the WAL frontier where the record is then appended, and no
//     record may be written in between), so a clean goopg restart does not
//     trip this.
//
// Where upstream PANICs on arm 2 while the state says DB_SHUTDOWNED
// ("invalid redo record in shutdown checkpoint"), goopg logs and recovers
// instead: refusing to start is the worse failure for a directory goopg may
// not have written, and replaying from an online checkpoint's redo is
// correct, merely slower.
//
// A genuinely absent or unreadable pg_control is tolerated the same way
// stampInProduction tolerates it — hand-assembled directories are a
// supported shape (verifyInitialized checks only PG_VERSION) — and yields
// the pre-M0131-S20 behaviour: replay everything the scan finds, stamp
// nothing.
func beginRecovery(dataDir string) (startupRecoveryDecision, error) {
	cd, err := control.ReadControlFile(dataDir)
	if err != nil || cd == nil {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("pg_control unreadable: starting without a control-file redo pointer",
				"dataDir", dataDir, "err", err)
		}
		return startupRecoveryDecision{}, nil
	}

	d := startupRecoveryDecision{
		redoLSN:   cd.CheckPointCopyRedo,
		prevState: cd.State,
		nextMulti: cd.CheckPointCopyNextMulti,
	}
	onlineCheckpoint := cd.CheckPointCopyRedo < cd.CheckPoint
	d.crashRecovery = cd.State != control.DBStateShutdowned || onlineCheckpoint
	if !d.crashRecovery {
		return d, nil
	}

	// Upstream's message, verbatim, so an operator's grep works across both
	// engines (xlogrecovery.c:957-958).
	slog.Warn("database system was not properly shut down; automatic recovery in progress",
		"dataDir", dataDir,
		"pg_control_state", control.DBStateName(cd.State),
		"redo", cd.CheckPointCopyRedo,
		"checkpoint", cd.CheckPoint,
		"lastCheckpointWasOnline", onlineCheckpoint)

	// Publish the in-recovery state BEFORE replaying. Upstream defers the
	// write until after subsystem init (xlogrecovery.c:946-947 "We don't
	// write the changes to disk yet"), but it holds the state in shared
	// memory the whole time; goopg has no such shadow, so the disk write is
	// the only way a concurrent pg_controldata — or a second crash midway
	// through replay — sees anything other than a stale DB_IN_PRODUCTION.
	if err := control.UpdateControlFile(dataDir, func(cd *control.ControlFileData) {
		cd.State = control.DBStateInCrashRecovery
	}); err != nil {
		return d, err
	}
	return d, nil
}

// clearRelcacheInitFiles sweeps every pg_internal.init in the cluster before
// replay, mirroring the unconditional RelationCacheInitFileRemove() upstream
// runs at xlog.c:5633. M0131-S20.3.
//
// Before this, goopg only ever unlinked init files REACTIVELY — one database
// at a time, from an invalidation it had itself generated
// (catalog.RelcacheInitFileUnlink). Nothing removed a file that was already on
// disk when the server started, so a PG-authored `global/pg_internal.init`
// survived into a goopg session and described a catalog that WAL replay was
// about to change. Upstream sweeps even after a clean shutdown ("it seems
// safest to just remove them always"), and so does this: the decision is
// deliberately NOT conditioned on decision.crashRecovery.
//
// Failure is logged, not fatal — the same elevel = LOG posture upstream's
// unlink_initfile is called with here. A file that could not be removed is a
// hazard, but so is refusing to open a cluster over it, and the reactive
// unlink path gets another chance at the first invalidation.
func clearRelcacheInitFiles(dataDir string) {
	err := catalog.WithRelCacheInitLock(func() error {
		return catalog.RelcacheInitFileRemoveAll(dataDir)
	})
	if err != nil {
		slog.Warn("could not remove some relcache init files before replay; a stale pg_internal.init may describe a pre-replay catalog",
			"dataDir", dataDir, "err", err)
	}
}

// endOfRecoveryCheckpoint forces a checkpoint after crash recovery has
// replayed, mirroring the end-of-recovery checkpoint upstream requests at
// StartupXLOG's tail (xlog.c:5959-5981, RequestCheckpoint with
// CHECKPOINT_END_OF_RECOVERY). It is what makes recovery durable: without it
// the redo pointer still addresses the pre-crash checkpoint, so a second
// crash before the first scheduled checkpoint (one checkpoint_timeout away,
// 300 s by default) replays the same span again — harmless but unbounded,
// and it leaves pg_control describing a cluster state that has already
// moved on.
//
// A failure here is logged rather than fatal: replay itself already
// succeeded and the data files are consistent, so refusing to start would
// deny service over bookkeeping that the next checkpoint redoes anyway.
func endOfRecoveryCheckpoint(cp *xlog.Checkpointer, dataDir string) {
	if cp == nil {
		return
	}
	if err := cp.CheckpointNow(); err != nil {
		slog.Warn("end-of-recovery checkpoint failed; recovery is complete but pg_control still points at the pre-crash checkpoint",
			"dataDir", dataDir, "err", err)
	}
}
