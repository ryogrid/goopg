package initdb

// Materialized-view query WAL replay (M0119-0004 pg_dump parity follow-up).
//
// catalog.Table.View is an in-memory parser AST; pg_class.relkind='m' (set by
// buildUserPGClassRow) tells loadUserTablesFromHeap the relation is a
// materialized view, but goopg writes no pg_rewrite-equivalent heap rows, so
// the defining SELECT and the IsPopulated flag cannot be reconstructed from
// the heap alone. Before this pass, a restarted matview reloaded as an
// ordinary relkind='r' table with View=nil — `CREATE MATERIALIZED VIEW`
// silently reverted to a plain table across a restart. The write side
// (executor syncTableToCatalogHeap) snapshots the view body as SQL text
// (tbl.ViewDef) in a RecordKindCreateMatView record; this pass re-parses it
// (parser.Parse) onto the reloaded table. Upstream analog: pg_rewrite
// (postgres/src/backend/rewrite/rewriteDefine.c).

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// replayMatViewRecords reads every WAL record under walDir and restores
// materialized-view query/populated state onto the reloaded catalog tables.
// Must run AFTER loadUserTablesFromHeap. A missing walDir is a no-op.
func replayMatViewRecords(walDir string, cat catalog.Catalog) error {
	im, ok := cat.(*catalog.InMemory)
	if !ok || im == nil {
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
		if len(rec.Payload) == 0 || rec.Payload[0] != wal.RecordKindCreateMatView {
			continue
		}
		p, derr := wal.DecodeMatView(rec.Payload)
		if derr != nil {
			return fmt.Errorf("decode matview at lsn %d: %w", rec.StartLSN, derr)
		}
		tbl, found := im.LookupTableByOID(p.TableOID)
		if !found || tbl == nil {
			continue // matview dropped since the snapshot
		}
		query, perr := parser.Parse(p.Query)
		if perr != nil || len(query) != 1 {
			// A deparse/reparse gap for an exotic query: skip this matview
			// rather than failing the whole startup — it degrades to the
			// pre-record state (reloaded as a plain table).
			continue
		}
		sel, ok := query[0].(*parser.SelectStmt)
		if !ok {
			continue
		}
		tbl.IsMatView = true
		tbl.View = sel
		tbl.ViewDef = p.Query
		tbl.IsPopulated = p.IsPopulated
	}
	return nil
}
