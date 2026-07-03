package initdb

// CREATE/ALTER/DROP ROLE WAL replay — the crash-recovery TAIL of the role
// persistence design (root-0021). The durable BASE is the pg_authid heap
// file (global/1260), rewritten by SyncPgAuthidFile on every role DDL and
// loaded back by LoadRolesFromAuthidHeap; this pass replays any role records
// still in the retained WAL ON TOP of the heap base (last-record-wins,
// idempotent), covering a crash between the WAL append and the heap file
// rename. Mirrors PostgreSQL's split: pg_authid heap = durable store, WAL =
// tail protecting it (postgres/src/backend/commands/user.c writes the heap;
// the physical WAL records replay onto it).

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// replayRoleDDLRecords reads every WAL record under walDir and applies role
// state / drop entries to the catalog role registry. Must run AFTER
// LoadRolesFromAuthidHeap (heap base first, WAL tail second). A missing
// walDir means "freshly initdb'd cluster" and is a no-op.
func replayRoleDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	im, ok := cat.(*catalog.InMemory)
	if !ok {
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

	for _, rec := range records {
		if len(rec.Payload) == 0 {
			continue
		}
		if len(rec.Payload) == 88 {
			// PG checkpoint records are a fixed 88-byte CheckPoint struct
			// with no leading kind-tag byte at all (wal.classifyXLogRecord
			// dispatches on length alone, format.go:234) — Payload[0] here
			// is just the low byte of the record's redo-LSN field, which
			// can coincidentally equal any RecordKind constant (discovered
			// via a real collision with RecordKindAlterRoleRename=72 on
			// the long-lived TPC-H bench WAL). Skip rather than risk
			// misdecoding a checkpoint as role DDL.
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindRoleState:
			p, derr := wal.DecodeRoleState(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode role-state at lsn %d: %w", rec.StartLSN, derr)
			}
			if p.OID != 0 {
				im.RegisterRoleWithOID(p.Name, p.OID)
			} else {
				im.RegisterRole(p.Name)
			}
			im.SetRoleAttrs(p.Name, catalog.RoleAttrs{
				CanLogin:  p.CanLogin,
				Superuser: p.Superuser,
				CredType:  p.CredType,
				Secret:    p.Secret,
			})
		case wal.RecordKindDropRole:
			name, derr := wal.DecodeDropRole(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-role at lsn %d: %w", rec.StartLSN, derr)
			}
			im.UnregisterRole(name)
		case wal.RecordKindAlterRoleRename:
			name, newName, derr := wal.DecodeAlterRoleRename(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode alter-role-rename at lsn %d: %w", rec.StartLSN, derr)
			}
			im.RenameRole(name, newName)
		}
	}
	return nil
}
