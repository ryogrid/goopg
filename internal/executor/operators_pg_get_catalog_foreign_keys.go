package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// pgGetCatalogForeignKeysOp implements pg_get_catalog_foreign_keys() as a
// FROM-clause SRF. Materialises one row per entry of sysFKRelationships on
// Open() — a static, catalog-independent table, mirroring PG's
// pg_get_catalog_foreign_keys (postgres/src/backend/utils/adt/misc.c).
// M0134-0146.
type pgGetCatalogForeignKeysOp struct {
	plan *optimizer.PgGetCatalogForeignKeys
	idx  int
}

func newPgGetCatalogForeignKeysOp(p *optimizer.PgGetCatalogForeignKeys) *pgGetCatalogForeignKeysOp {
	return &pgGetCatalogForeignKeysOp{plan: p}
}

func (o *pgGetCatalogForeignKeysOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *pgGetCatalogForeignKeysOp) Open(_ *Context) error {
	o.idx = 0
	return nil
}

func (o *pgGetCatalogForeignKeysOp) Next() (TupleSlot, error) {
	if o.idx >= len(sysFKRelationships) {
		return nil, EOF
	}
	fk := sysFKRelationships[o.idx]
	o.idx++
	row := Row{
		NewIntDatum(int64(fk.FKTable)),
		NewStringDatum("{" + strings.Join(fk.FKCols, ",") + "}"),
		NewIntDatum(int64(fk.PKTable)),
		NewStringDatum("{" + strings.Join(fk.PKCols, ",") + "}"),
		NewBoolDatum(fk.IsArray),
		NewBoolDatum(fk.IsOpt),
	}
	return SlotFromRow(nil, row), nil
}

func (o *pgGetCatalogForeignKeysOp) Close() error { return nil }
