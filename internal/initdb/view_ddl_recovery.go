package initdb

// Plain-view query WAL replay (M0119-0004 pg_dump parity follow-up, sibling of
// matview_ddl_recovery.go).
//
// catalog.Table.View is an in-memory parser AST; pg_class.relkind='v' (set by
// buildUserPGClassRow) tells loadUserTablesFromHeap the relation is a plain
// view (so it registers a Virtual, storage-less Table from pg_attribute's
// column list), but the defining SELECT itself lives only in tbl.View — goopg
// writes no pg_rewrite-equivalent heap rows, so it must be snapshotted as SQL
// text the same way M0119-0004's CREATE MATERIALIZED VIEW record is. Before
// this pass (and its write-side counterpart, syncTableToCatalogHeap never
// being called from execCreateView at all), a plain view had zero on-disk
// persistence: it simply ceased to exist after a restart. Upstream analog:
// pg_rewrite (postgres/src/backend/rewrite/rewriteDefine.c).

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// replayViewRecords reads every WAL record under walDir and restores plain
// view query state onto the reloaded catalog tables. Must run AFTER
// loadUserTablesFromHeap. A missing walDir is a no-op.
func replayViewRecords(walDir string, cat catalog.Catalog) error {
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
		if len(rec.Payload) == 0 || rec.Payload[0] != wal.RecordKindCreateView {
			continue
		}
		p, derr := wal.DecodeView(rec.Payload)
		if derr != nil {
			return fmt.Errorf("decode view at lsn %d: %w", rec.StartLSN, derr)
		}
		tbl, found := im.LookupTableByOID(p.TableOID)
		if !found || tbl == nil {
			continue // view dropped (or replaced under a new OID) since the snapshot
		}
		query, perr := parser.Parse(p.Query)
		if perr != nil || len(query) != 1 {
			// A deparse/reparse gap for an exotic query: skip this view rather
			// than failing the whole startup — it degrades to the pre-record
			// state (reloaded with columns but no substitutable query).
			continue
		}
		sel, ok := query[0].(*parser.SelectStmt)
		if !ok {
			continue
		}
		tbl.View = sel
		tbl.ViewDef = p.Query
	}
	return nil
}
