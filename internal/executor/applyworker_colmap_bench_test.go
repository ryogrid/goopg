package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

func benchApplyRel(ncols int) *applyRel {
	remote := &xlog.DecodedRelation{}
	local := &catalog.Table{Name: "t"}
	for i := 0; i < ncols; i++ {
		name := fmt.Sprintf("col%02d", i)
		remote.Columns = append(remote.Columns, xlog.DecodedAttr{Name: name})
		local.Columns = append(local.Columns, catalog.Column{Name: name, Type: catalog.Type{Name: "text"}})
	}
	return &applyRel{remote: remote, local: local}
}

func benchApplyTuple(ncols int) []xlog.DecodedColumn {
	tup := make([]xlog.DecodedColumn, ncols)
	for i := range tup {
		tup[i] = xlog.DecodedColumn{Status: 't', Bytes: []byte("value")}
	}
	return tup
}

// BenchmarkApplyDecodeTuple measures decoding one replicated row
// (review/260831 EC-3): the remote->local column map used to be rebuilt from
// the column names for every row.
func BenchmarkApplyDecodeTuple(b *testing.B) {
	const ncols = 16
	r := benchApplyRel(ncols)
	tup := benchApplyTuple(ncols)

	b.Run("cached-map", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, _, err := r.decodeTuple(tup); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("map-per-row", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, _, err := decodePgoutputTupleAsRow(r.remote.Columns, r.local.Columns, tup); err != nil {
				b.Fatal(err)
			}
		}
	})
}
