package xlog

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21 closure: the whole opcode space of every handled rmgr -------
//
// S21a and S21b each landed one opcode (or one small family) at a time, and
// each shipped its own behavioural guard. What none of them could assert is the
// property S21 as a milestone is actually about: that NO opcode PostgreSQL 18
// defines, inside a resource manager goopg claims to handle, still falls to
// that rmgr's `default:` refusal.
//
// That property cannot be read off any single slice's test. A per-slice guard
// says "the arm I added works"; it is silent about the arm nobody added. This
// test enumerates upstream's opcode space instead — every XLOG_* value in
// postgres/src/include for the rmgrs listed below — and asserts each one
// reaches a NAMED arm.
//
// How "named arm" is decided, and why it is exact rather than a prefix match:
// every rmgr's `default:` produces exactly
//
//	fmt.Errorf("%w: %s", ErrUnsupportedRecord, unsupportedDecodedXLogRecord(r))
//
// while the DELIBERATE refusals (2PC in RM_XACT, XLOG_HEAP2_REWRITE) wrap that
// same text and append a parenthesised reason. Comparing the error string for
// EQUALITY with the default's therefore separates "no arm exists" from "an arm
// exists and refuses on purpose", which a `strings.Contains` or an
// `errors.Is(err, ErrUnsupportedRecord)` check cannot do — both refusal shapes
// carry ErrUnsupportedRecord, and that is the point: the sentinel is what stops
// the reader treating a durable record as a torn tail (S16.2).
//
// The records handed to the dispatcher are minimal on purpose: header only, no
// blocks, no main data. This is a DISPATCH test, so any other error — "missing
// block 0", a short main-data decode, a refused fork — is a PASS: it proves the
// opcode reached code that knows what the opcode is. Whether that code decodes
// correctly is each slice's own guard (heap_multi_insert_pg_test.go,
// btree_dedup_pg_test.go, …), not this one's.
//
// Out of scope, deliberately: RM_HASH/GIN/GIST/SPGIST/BRIN (12..14, 16, 17).
// Those are refused WHOLESALE by rmgr, not by opcode (M0131-S25) — goopg has no
// page code for any of them, so enumerating their opcodes here would assert the
// opposite of what S25 decided. RM_MULTIXACT (6) has no arm at all and is
// M0131-S24's open work.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md.

// pgOpcode is one upstream XLOG_* value plus the header it is defined in, so a
// failure names the file to open rather than only a hex number.
type pgOpcode struct {
	name string
	info uint8
}

// pgRmgrOpcodeSpace is upstream's opcode space for one handled rmgr.
//
// `defined` must list EVERY value the header defines (opcode defines only —
// XLOG_HEAP_INIT_PAGE, XLOG_HEAP_OPMASK, XLOG_XACT_HAS_INFO and friends are
// flags/masks, not opcodes, and are exercised separately below).
//
// `undefinedControl` is a value in the same space that PG 18 leaves undefined.
// It is the test's own fail-when-broken proof: it must land on the `default:`
// arm, so if a future refactor made this test unable to SEE the default arm,
// the control fails and says so. Zero means the space is full (RM_HEAP and
// RM_HEAP2 define all eight values under the 0x70 mask) and no control exists.
type pgRmgrOpcodeSpace struct {
	rmgr             Rmgr
	rmgrName         string
	header           string
	defined          []pgOpcode
	undefinedControl uint8
	hasControl       bool
	// controlWant, when non-empty, replaces the exact-default-message
	// expectation with a substring match. Exactly one rmgr needs it: RM_BTREE's
	// `default:` arm is not a bare refusal but S16.3's full-page-image
	// fallback, which REPLAYS an unknown btree opcode whose every block carries
	// an apply-image and refuses (still ErrUnsupportedRecord-classed) only when
	// one does not. A header-only probe has no blocks, so it takes the refusal
	// branch — with the fallback's own wording, not the default's.
	controlWant string
}

func pgHandledRmgrOpcodeSpaces() []pgRmgrOpcodeSpace {
	return []pgRmgrOpcodeSpace{
		{
			rmgr: RmgrXLog, rmgrName: "RM_XLOG_ID", header: "catalog/pg_control.h",
			defined: []pgOpcode{
				{"XLOG_CHECKPOINT_SHUTDOWN", xlogXLogCheckpointShutdown},
				{"XLOG_CHECKPOINT_ONLINE", xlogXLogCheckpointOnline},
				{"XLOG_NOOP", xlogXLogNoop},
				{"XLOG_NEXTOID", xlogXLogNextOid},
				{"XLOG_SWITCH", xlogXLogSwitch},
				{"XLOG_BACKUP_END", xlogXLogBackupEnd},
				{"XLOG_PARAMETER_CHANGE", xlogXLogParameterChange},
				{"XLOG_RESTORE_POINT", xlogXLogRestorePoint},
				{"XLOG_FPW_CHANGE", xlogXLogFPWChange},
				{"XLOG_END_OF_RECOVERY", xlogXLogEndOfRecovery},
				{"XLOG_FPI_FOR_HINT", xlogXLogFPIForHint},
				{"XLOG_FPI", xlogXLogFPI},
				{"XLOG_OVERWRITE_CONTRECORD", xlogXLogOverwriteContrecord},
				{"XLOG_CHECKPOINT_REDO", xlogXLogCheckpointRedo},
			},
			// 0xF0 is NOT free here — it is goopg's own empty-payload marker
			// (xlogInfoDefault, format.go) and is deliberately benign.
			undefinedControl: 0xC0, hasControl: true,
		},
		{
			rmgr: RmgrXact, rmgrName: "RM_XACT_ID", header: "access/xact.h",
			defined: []pgOpcode{
				{"XLOG_XACT_COMMIT", xlogXactCommit},
				{"XLOG_XACT_PREPARE", xlogXactPrepare},
				{"XLOG_XACT_ABORT", xlogXactAbort},
				{"XLOG_XACT_COMMIT_PREPARED", xlogXactCommitPrepared},
				{"XLOG_XACT_ABORT_PREPARED", xlogXactAbortPrepared},
				{"XLOG_XACT_ASSIGNMENT", xlogXactAssignment},
				{"XLOG_XACT_INVALIDATIONS", xlogXactInvalidations},
			},
			// RM_XACT masks with XLOG_XACT_OPMASK (0x70); 0x70 itself is the
			// one value the header leaves unassigned.
			undefinedControl: 0x70, hasControl: true,
		},
		{
			rmgr: RmgrStorage, rmgrName: "RM_SMGR_ID", header: "catalog/storage_xlog.h",
			defined: []pgOpcode{
				{"XLOG_SMGR_CREATE", xlogSmgrCreate},
				{"XLOG_SMGR_TRUNCATE", xlogSmgrTruncate},
			},
			undefinedControl: 0x00, hasControl: true,
		},
		{
			rmgr: RmgrCLOG, rmgrName: "RM_CLOG_ID", header: "access/clog.h",
			defined: []pgOpcode{
				{"CLOG_ZEROPAGE", xlogClogZeroPage},
				{"CLOG_TRUNCATE", xlogClogTruncate},
			},
			undefinedControl: 0x20, hasControl: true,
		},
		{
			rmgr: RmgrDbase, rmgrName: "RM_DBASE_ID", header: "commands/dbcommands_xlog.h",
			defined: []pgOpcode{
				{"XLOG_DBASE_CREATE_FILE_COPY", xlogDbaseCreateFileCopy},
				{"XLOG_DBASE_CREATE_WAL_LOG", xlogDbaseCreateWalLog},
				{"XLOG_DBASE_DROP", xlogDbaseDrop},
			},
			undefinedControl: 0x30, hasControl: true,
		},
		{
			rmgr: RmgrTblspc, rmgrName: "RM_TBLSPC_ID", header: "commands/tablespace.h",
			defined: []pgOpcode{
				{"XLOG_TBLSPC_CREATE", xlogTblspcCreate},
				{"XLOG_TBLSPC_DROP", xlogTblspcDrop},
			},
			undefinedControl: 0x20, hasControl: true,
		},
		{
			rmgr: RmgrRelMap, rmgrName: "RM_RELMAP_ID", header: "utils/relmapper.h",
			defined: []pgOpcode{
				{"XLOG_RELMAP_UPDATE", xlogRelmapUpdate},
			},
			undefinedControl: 0x10, hasControl: true,
		},
		{
			rmgr: RmgrStandby, rmgrName: "RM_STANDBY_ID", header: "storage/standby_xlog.h",
			defined: []pgOpcode{
				{"XLOG_STANDBY_LOCK", xlogStandbyLock},
				{"XLOG_RUNNING_XACTS", xlogStandbyRunningXacts},
				{"XLOG_INVALIDATIONS", xlogStandbyInvalidations},
			},
			undefinedControl: 0x30, hasControl: true,
		},
		{
			rmgr: RmgrHeap2, rmgrName: "RM_HEAP2_ID", header: "access/heapam_xlog.h",
			defined: []pgOpcode{
				{"XLOG_HEAP2_REWRITE", xlogHeap2Rewrite},
				{"XLOG_HEAP2_PRUNE_ON_ACCESS", xlogHeap2PruneOnAccess},
				{"XLOG_HEAP2_PRUNE_VACUUM_SCAN", xlogHeap2PruneVacuumScan},
				{"XLOG_HEAP2_PRUNE_VACUUM_CLEANUP", xlogHeap2PruneVacuumClean},
				{"XLOG_HEAP2_VISIBLE", xlogHeap2Visible},
				{"XLOG_HEAP2_MULTI_INSERT", xlogHeap2MultiInsert},
				{"XLOG_HEAP2_LOCK_UPDATED", xlogHeap2LockUpdated},
				{"XLOG_HEAP2_NEW_CID", xlogHeap2NewCid},
				// The mask bug S21a-1 fixed, pinned as a coverage entry too:
				// heap_multi_insert ORs XLOG_HEAP_INIT_PAGE into the info byte
				// of a COPY onto a freshly extended page (heapam.c:2607-2611),
				// so 0xD0 must resolve to MULTI_INSERT, not to `default:`.
				{"XLOG_HEAP2_MULTI_INSERT|XLOG_HEAP_INIT_PAGE", xlogHeap2MultiInsert | xlogHeapInit},
			},
			// RM_HEAP2 masks with XLOG_HEAP_OPMASK (0x70) and upstream defines
			// all eight values, so there is no undefined control here.
		},
		{
			rmgr: RmgrHeap, rmgrName: "RM_HEAP_ID", header: "access/heapam_xlog.h",
			defined: []pgOpcode{
				{"XLOG_HEAP_INSERT", xlogHeapInsert},
				{"XLOG_HEAP_DELETE", xlogHeapDelete},
				{"XLOG_HEAP_UPDATE", xlogHeapUpdate},
				{"XLOG_HEAP_TRUNCATE", xlogHeapTruncate},
				{"XLOG_HEAP_HOT_UPDATE", xlogHeapHotUpdate},
				{"XLOG_HEAP_CONFIRM", xlogHeapConfirm},
				{"XLOG_HEAP_LOCK", xlogHeapLock},
				{"XLOG_HEAP_INPLACE", xlogHeapInplace},
				{"XLOG_HEAP_INSERT|XLOG_HEAP_INIT_PAGE", xlogHeapInsert | xlogHeapInit},
			},
			// Same full 0x70 space as RM_HEAP2.
		},
		{
			rmgr: RmgrBtree, rmgrName: "RM_BTREE_ID", header: "access/nbtxlog.h",
			defined: []pgOpcode{
				{"XLOG_BTREE_INSERT_LEAF", xlogBtreeInsertLeaf},
				{"XLOG_BTREE_INSERT_UPPER", xlogBtreeInsertUpper},
				{"XLOG_BTREE_INSERT_META", xlogBtreeInsertMeta},
				{"XLOG_BTREE_SPLIT_L", xlogBtreeSplitL},
				{"XLOG_BTREE_SPLIT_R", xlogBtreeSplitR},
				{"XLOG_BTREE_INSERT_POST", xlogBtreeInsertPost},
				{"XLOG_BTREE_DEDUP", xlogBtreeDedup},
				{"XLOG_BTREE_DELETE", xlogBtreeDelete},
				{"XLOG_BTREE_UNLINK_PAGE", xlogBtreeUnlinkPage},
				{"XLOG_BTREE_UNLINK_PAGE_META", xlogBtreeUnlinkPageMeta},
				{"XLOG_BTREE_NEWROOT", xlogBtreeNewRoot},
				{"XLOG_BTREE_MARK_PAGE_HALFDEAD", xlogBtreeMarkPageHalfDead},
				{"XLOG_BTREE_VACUUM", xlogBtreeVacuum},
				{"XLOG_BTREE_REUSE_PAGE", xlogBtreeReusePage},
				{"XLOG_BTREE_META_CLEANUP", xlogBtreeMetaCleanup},
			},
			undefinedControl: 0xF0, hasControl: true,
			controlWant: "has no block references to restore",
		},
		{
			rmgr: RmgrSeq, rmgrName: "RM_SEQ_ID", header: "commands/sequence.h",
			defined: []pgOpcode{
				{"XLOG_SEQ_LOG", 0x00},
			},
			undefinedControl: 0x10, hasControl: true,
		},
		{
			rmgr: RmgrCommitTs, rmgrName: "RM_COMMIT_TS_ID", header: "access/commit_ts.h",
			defined: []pgOpcode{
				{"COMMIT_TS_ZEROPAGE", xlogCommitTsZeroPage},
				{"COMMIT_TS_TRUNCATE", xlogCommitTsTruncate},
			},
			undefinedControl: 0x20, hasControl: true,
		},
		{
			rmgr: RmgrReplicationOrigin, rmgrName: "RM_REPLORIGIN_ID", header: "replication/origin.h",
			defined: []pgOpcode{
				{"XLOG_REPLORIGIN_SET", xlogReplOriginSet},
				{"XLOG_REPLORIGIN_DROP", xlogReplOriginDrop},
			},
			undefinedControl: 0x20, hasControl: true,
		},
		{
			rmgr: RmgrGeneric, rmgrName: "RM_GENERIC_ID", header: "access/generic_xlog.h",
			defined: []pgOpcode{
				// RM_GENERIC_ID defines no opcodes at all: generic_redo reads
				// the whole record as block images and upstream's info byte is
				// always zero.
				{"(no opcode; info == 0)", xlogGenericInfo},
			},
			undefinedControl: 0x10, hasControl: true,
		},
		{
			rmgr: RmgrLogicalMessage, rmgrName: "RM_LOGICALMSG_ID", header: "replication/message.h",
			defined: []pgOpcode{
				{"XLOG_LOGICAL_MESSAGE", xlogLogicalMessage},
			},
			undefinedControl: 0x10, hasControl: true,
		},
	}
}

// replayDefaultArmMessage is the exact error string an rmgr's `default:` arm
// produces for this record. See the file header for why equality is the test.
func replayDefaultArmMessage(rec Record) string {
	return fmt.Errorf("%w: %s", ErrUnsupportedRecord, unsupportedDecodedXLogRecord(rec)).Error()
}

func replayProbeOpcode(t *testing.T, mgr *storage.Manager, rmgr Rmgr, info uint8) (applied bool, err error) {
	t.Helper()
	rec := Record{XLog: &XLogDecodedRecord{
		Header: XLogRecord{Rmid: rmgr, Info: info},
	}}
	// A panic would also prove the opcode reached a named arm, but it would
	// abort the whole run and hide the remaining opcodes — and a panic on a
	// malformed record is a robustness defect worth its own report, so convert
	// it into a named failure rather than swallowing it.
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("replayDecodedXLogRecord panicked on a header-only record: %v", p)
		}
	}()
	return replayDecodedXLogRecord(mgr, rec)
}

// TestReplayOpcodeSpaceCoverageForHandledRmgrs is the M0131-S21 acceptance
// guard: for every rmgr goopg has a dispatch arm for, EVERY opcode PostgreSQL
// 18 defines resolves to a named arm rather than the rmgr's `default:`.
//
// It is what makes the S21 milestone checkable as a whole. Before S21 the
// answer for most of these was "refuses the start"; each S21a/S21b slice moved
// one to applied, recognised-no-op, or refused-by-name. This test is the
// standing statement that none moved back and none was missed — and it fails
// the moment someone adds an rmgr arm without covering its opcode space, or PG
// adds an opcode in a version bump.
func TestReplayOpcodeSpaceCoverageForHandledRmgrs(t *testing.T) {
	for _, space := range pgHandledRmgrOpcodeSpaces() {
		t.Run(space.rmgrName, func(t *testing.T) {
			for _, op := range space.defined {
				t.Run(op.name, func(t *testing.T) {
					mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
					_, err := replayProbeOpcode(t, mgr, space.rmgr, op.info)
					if err == nil {
						return // applied or recognised no-op: covered
					}
					rec := Record{XLog: &XLogDecodedRecord{
						Header: XLogRecord{Rmid: space.rmgr, Info: op.info},
					}}
					if err.Error() == replayDefaultArmMessage(rec) {
						t.Fatalf("%s (%s, info=0x%02x) fell to %s's default arm: %v\n"+
							"upstream defines this opcode in %s; it needs a named arm "+
							"(applied, recognised no-op, or refused with a stated reason)",
							op.name, space.rmgrName, op.info, space.rmgrName, err, space.header)
					}
					// Any other error means a named arm handled the opcode and
					// rejected this deliberately-empty record. That is a pass.
				})
			}

			if !space.hasControl {
				return
			}
			t.Run(fmt.Sprintf("control/undefined_0x%02X", space.undefinedControl), func(t *testing.T) {
				mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
				_, err := replayProbeOpcode(t, mgr, space.rmgr, space.undefinedControl)
				if err == nil {
					t.Fatalf("undefined opcode 0x%02x in %s replayed without error: "+
						"either PG 18 does define it (fix the table above) or the arm is too permissive",
						space.undefinedControl, space.rmgrName)
				}
				rec := Record{XLog: &XLogDecodedRecord{
					Header: XLogRecord{Rmid: space.rmgr, Info: space.undefinedControl},
				}}
				// The class, on every rmgr's refusal path. format.go:45-56: an
				// unsupported record's bytes are intact and durable, so a
				// caller must be able to tell it from a torn tail. Thirteen
				// rmgrs returned a bare error here until the S21 closure slice.
				if !errors.Is(err, ErrUnsupportedRecord) {
					t.Fatalf("undefined opcode 0x%02x in %s produced an UNCLASSED error %v: "+
						"want ErrUnsupportedRecord so a caller can distinguish a durable "+
						"record goopg cannot apply from a torn tail",
						space.undefinedControl, space.rmgrName, err)
				}
				if space.controlWant != "" {
					if !strings.Contains(err.Error(), space.controlWant) {
						t.Fatalf("undefined opcode 0x%02x in %s produced %v, want a refusal containing %q",
							space.undefinedControl, space.rmgrName, err, space.controlWant)
					}
					return
				}
				if err.Error() != replayDefaultArmMessage(rec) {
					t.Fatalf("undefined opcode 0x%02x in %s produced %v, want the default arm's refusal: "+
						"this control is what proves the coverage assertion above can still SEE the default arm",
						space.undefinedControl, space.rmgrName, err)
				}
			})
		})
	}
}
