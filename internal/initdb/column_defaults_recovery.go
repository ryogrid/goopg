package initdb

// Column DEFAULT expression WAL replay (root-0020 follow-up).
//
// catalog.Column.DefaultExpr is an in-memory parser AST; the pg_attribute
// heap carries only atthasdef, and goopg writes no runtime pg_attrdef rows,
// so loadUserTablesFromHeap reloads defaulted columns with DefaultExpr=nil.
// Before this pass, an INSERT omitting a defaulted column inserted NULL after
// a restart — WordPress's `wp_posts.comment_count bigint NOT NULL DEFAULT 0`
// raised a NOT NULL violation on every post-restart post creation. The write
// side (executor syncTableToCatalogHeap) snapshots each table's defaults as
// SQL text (catalog.FormatExprForAttrdef) in a RecordKindColumnDefaults
// record; this pass re-parses them (parser.ParseExpr) onto the reloaded
// columns, last-record-wins. Upstream analog: pg_attrdef
// (postgres/src/backend/catalog/heap.c StoreAttrDefault).

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// replayColumnDefaultsRecords reads every WAL record under walDir and
// restores column DEFAULT expressions onto the reloaded catalog tables. Must
// run AFTER loadUserTablesFromHeap. A missing walDir is a no-op.
func replayColumnDefaultsRecords(walDir string, cat catalog.Catalog) error {
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
		if len(rec.Payload) == 0 || rec.Payload[0] != wal.RecordKindColumnDefaults {
			continue
		}
		p, derr := wal.DecodeColumnDefaults(rec.Payload)
		if derr != nil {
			return fmt.Errorf("decode column-defaults at lsn %d: %w", rec.StartLSN, derr)
		}
		tbl, _, found := im.LookupTableByOIDAllDBs(p.TableOID)
		if !found || tbl == nil {
			continue // table dropped since the snapshot
		}
		for _, d := range p.Defaults {
			expr, perr := parser.ParseExpr(d.Expr)
			if perr != nil {
				// A deparse/reparse gap for an exotic expression: skip the
				// single column rather than failing the whole startup —
				// behavior degrades to the pre-record state (NULL default).
				continue
			}
			for i := range tbl.Columns {
				if strings.EqualFold(tbl.Columns[i].Name, d.Name) {
					tbl.Columns[i].DefaultExpr = expr
					break
				}
			}
		}
	}
	return nil
}
