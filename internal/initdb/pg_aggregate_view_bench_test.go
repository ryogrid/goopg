package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// BenchmarkPgAggregateVirtualRows measures one query's worth of pg_aggregate
// (review/260831 IN-4): the 161 constant BKI rows used to be rebuilt and
// re-formatted — 22 columns of Sprintf and OID->name lookups each — on every
// query, instead of once.
func BenchmarkPgAggregateVirtualRows(b *testing.B) {
	cat := catalog.NewInMemory()
	if err := registerPgAggregateView(cat); err != nil {
		b.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_aggregate"})
	if !ok {
		b.Fatal("pg_aggregate not registered")
	}
	b.ReportAllocs()
	for b.Loop() {
		if len(tbl.VirtualRows()) < 161 {
			b.Fatal("missing BKI rows")
		}
	}
}
