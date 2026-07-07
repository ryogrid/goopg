package initdb

// CREATE/DROP/ALTER FUNCTION and PROCEDURE WAL replay (DU-002
// restart-persistence follow-up to M0119-0004, loop #71 ledger resume
// point — the "newly discovered while verifying live" gap: CREATE FUNCTION
// itself had no WAL/restart persistence, so an event trigger's evtfoid
// could dangle post-restart).
//
// Physical WAL replay (`wal.ReplayFromDirWithMgr`) ignores the function
// record kinds (61-64) because goopg has no per-routine file namespace —
// catalog.Routines is a pure in-memory registry with no page-level state to
// reconstruct. It is, however, the source of truth behind the pg_proc
// virtual view (pg_dump's function/procedure dump, and any event trigger's
// evtfoid reference), so losing it on every restart is a visible
// regression. This recovery pass walks the WAL once after physical replay
// finishes, decodes each function DDL record, and applies it to the
// catalog so the post-restart server agrees with what the pre-crash server
// told the client. Mirrors replayEventTriggerDDLRecords
// (internal/initdb/event_trigger_ddl_recovery.go); routines, like event
// triggers, are keyed by a plain schema NAME string (catalog.Routine has no
// NamespaceOID to resolve), so this does not depend on schema replay
// having run first.
//
// Scope: CREATE/OR REPLACE FUNCTION, CREATE/OR REPLACE PROCEDURE, ALTER
// FUNCTION/PROCEDURE/ROUTINE (RENAME TO and the four mutable attributes),
// and DROP FUNCTION *and* DROP PROCEDURE (both the autocommit-immediate and
// the deferred-to-COMMIT removal paths, plus CASCADE-dependent drops) — DROP
// PROCEDURE was unified onto DROP FUNCTION's deferred-until-COMMIT
// transactional model (loop #74), so both share the same
// ApplyDeferredRoutineDrops WAL-logging chokepoint.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/wal"
)

// replayFunctionDDLRecords reads every WAL record under walDir and applies
// CREATE/DROP/ALTER FUNCTION/PROCEDURE entries to cat. A missing walDir
// means "freshly initdb'd cluster" and is treated as a no-op. cat may be
// nil (some embedded test setups), in which case the function returns nil
// without doing any I/O.
func replayFunctionDDLRecords(walDir string, cat *catalog.InMemory) error {
	if cat == nil {
		return nil
	}
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat wal dir: %w", err)
	}

	records, err := wal.ReadAll(walDir, 0)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	rs := cat.Routines()
	if rs == nil {
		return nil
	}

	for _, rec := range records {
		if len(rec.Payload) == 0 || !wal.IsGoopgNativeRecord(rec) {
			// wal.IsGoopgNativeRecord rules out a PG-native/canonical
			// XLogRecord whose MainData is raw PG struct bytes with no
			// goopg kind-byte tag at Payload[0] — scanning it against the
			// small custom RecordKind constants below risks a coincidental
			// false-positive byte match (M0106-0011), so skip these
			// records entirely.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindCreateFunction:
			p, derr := wal.DecodeCreateFunction(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-function at lsn %d: %w", rec.StartLSN, derr)
			}
			r := createFunctionPayloadToRoutine(p)
			stored := rs.CreateDuringRecovery(r)
			if strings.ToLower(stored.Language) == "sql" {
				executor.ExtractRoutineDeps(stored.Body, stored.ArgDefaults, stored.Schema, stored, cat)
			}
		case wal.RecordKindDropFunction:
			oid, derr := wal.DecodeDropFunction(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-function at lsn %d: %w", rec.StartLSN, derr)
			}
			rs.DropByOIDDuringRecovery(oid)
		case wal.RecordKindAlterFunctionRename:
			oid, newName, derr := wal.DecodeAlterFunctionRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-function-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			rs.RenameByOIDDuringRecovery(oid, newName)
		case wal.RecordKindAlterFunctionFlags:
			oid, volatile, securityDefiner, leakproof, strict, derr := wal.DecodeAlterFunctionFlags(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-function-flags at lsn %d: %w", rec.StartLSN, derr)
			}
			rs.SetFlagsByOIDDuringRecovery(oid, volatile, securityDefiner, leakproof, strict)
		case wal.RecordKindAlterFunctionOwner:
			oid, ownerOID, derr := wal.DecodeAlterFunctionOwner(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-function-owner at lsn %d: %w", rec.StartLSN, derr)
			}
			rs.SetOwnerByOIDDuringRecovery(oid, ownerOID)
		case wal.RecordKindAlterFunctionSetSchema:
			oid, newSchema, derr := wal.DecodeAlterFunctionSetSchema(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-function-set-schema at lsn %d: %w", rec.StartLSN, derr)
			}
			rs.SetSchemaByOIDDuringRecovery(oid, newSchema)
		}
	}
	return nil
}

// createFunctionPayloadToRoutine converts a decoded WAL payload back into
// the parallel-slice shape catalog.Routine stores. Mirror of
// executor.routineToCreateFunctionPayload's inverse.
func createFunctionPayloadToRoutine(p wal.CreateFunctionPayload) *catalog.Routine {
	argNames := make([]string, len(p.Args))
	argTypes := make([]catalog.Type, len(p.Args))
	argModes := make([]string, len(p.Args))
	argDefaults := make([]string, len(p.Args))
	for i, a := range p.Args {
		argNames[i] = a.Name
		argTypes[i] = catalog.Type{Name: a.TypeName, Args: a.TypeArgs}
		argModes[i] = a.Mode
		argDefaults[i] = a.Default
	}
	return &catalog.Routine{
		OID:             p.OID,
		Schema:          p.Schema,
		Name:            p.Name,
		ArgNames:        argNames,
		ArgTypes:        argTypes,
		ArgModes:        argModes,
		ArgDefaults:     argDefaults,
		ReturnType:      catalog.Type{Name: p.ReturnTypeName, Args: p.ReturnTypeArgs},
		ReturnsSet:      p.ReturnsSet,
		ReturnsTable:    p.ReturnsTable,
		Language:        p.Language,
		Body:            p.Body,
		Strict:          p.Strict,
		Volatile:        p.Volatile,
		Parallel:        p.Parallel,
		Cost:            p.Cost,
		Rows:            p.Rows,
		SecurityDefiner: p.SecurityDefiner,
		Leakproof:       p.Leakproof,
		IsProcedure:     p.IsProcedure,
		IsWindow:        p.IsWindow,
		BeginAtomic:     p.BeginAtomic,
		IsReturnForm:    p.IsReturnForm,
		KindChar:        p.KindChar,
	}
}
