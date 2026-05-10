package initdb

// CREATE/DROP INDEX WAL replay (M0079-0001).
//
// goopg has no `pg_index` heap relation — `syncIndexToCatalogHeap`
// writes only a `pg_class` row with `relkind='i'` and the index
// OID/name. That is not enough to fully reconstruct an
// `*catalog.Index`: the column list, unique flag, primary flag,
// and owning-table OID would all have to come from a sibling
// pg_index relation that does not exist.
//
// To bridge the gap without inventing a new heap relation,
// `execCreateIndex` (and `execDropIndex`) emit a custom WAL
// record (`wal.RecordKindCreateIndex` / `RecordKindDropIndex`)
// that carries the full metadata. Physical WAL replay
// (`wal.ReplayFromDirWithMgr`) treats those record kinds as
// no-ops: the on-disk btree pages are already restored by
// `RecordKindBtreeInsert` / `RecordKindSmgrCreate`, and the
// pg_class row is restored by `RecordKindHeapInsert`. After
// physical replay, this driver walks the WAL once more, decodes
// every CREATE/DROP INDEX record, and applies it to the
// in-memory catalog so the post-restart server can find the
// index and the planner can emit IndexScans again.
//
// Mirrors `replayDatabaseDDLRecords` exactly (M0054-0001) — the
// CreateDatabase pattern is the established template for
// catalog-only WAL records in goopg.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// indexRegistryRecovery is the catalog-side surface this
// recovery pass needs. `*catalog.InMemory` satisfies it.
type indexRegistryRecovery interface {
	RegisterIndexDuringRecovery(schema, name string, tableOID uint32, cols []string, unique bool, method string, primary bool, oid uint32)
	UnregisterIndexDuringRecovery(schema, name string)
}

// replayIndexDDLRecords reads every WAL record under walDir and
// applies CREATE / DROP INDEX entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a
// no-op. The catalog argument may be nil in some embedded test
// setups; this is also a no-op.
//
// Order matters: a DROP following a CREATE cancels out, so we
// walk records in stream order — the same discipline
// `replayDatabaseDDLRecords` uses.
//
// (M0079-0001.)
func replayIndexDDLRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(indexRegistryRecovery)
	if !ok {
		// Catalog implementation does not expose the recovery
		// hooks; nothing to do. (catalog.InMemory does.)
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
		switch rec.Payload[0] {
		case wal.RecordKindCreateIndex:
			p, derr := wal.DecodeCreateIndex(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode create-index at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RegisterIndexDuringRecovery(
				p.Schema,
				p.Name,
				p.TableOID,
				p.Columns,
				p.Unique,
				p.Method,
				p.Primary,
				p.OID,
			)
		case wal.RecordKindDropIndex:
			p, derr := wal.DecodeDropIndex(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode drop-index at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.UnregisterIndexDuringRecovery(p.Schema, p.Name)
		}
	}
	return nil
}
