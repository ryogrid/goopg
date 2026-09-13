package optimizer

import (
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

// pgHashTupleSpillCost is an R108 measurement switch. Only the exact value
// "1" enables the alternative planner price; unset and every other spelling
// retain the executor-map geometry path.
var pgHashTupleSpillCost = pgHashTupleSpillCostFromEnv(os.Getenv("GOOPG_PG_HASH_TUPLE_SPILL_COST"))

func pgHashTupleSpillCostFromEnv(v string) bool { return v == "1" }

func pgHashTupleSpillCostEnabled() bool { return pgHashTupleSpillCost }

func setPGHashTupleSpillCostForTest(on bool) func() {
	old := pgHashTupleSpillCost
	pgHashTupleSpillCost = on
	return func() { pgHashTupleSpillCost = old }
}

// pgHashTupleSpillGeometry is the one R108 branch decision shared by costing
// and trace. usePG is true only when the opt-in switch, packed hash geometry,
// and both PG heap-page calculations are valid.
type pgHashTupleSpillGeometry struct {
	usePG                 bool
	hash                  pgHashGeometryResult
	outerPages, innerPages float64
}

func pgHashTupleSpillGeometryFor(in hashJoinInputs, cp costParams) pgHashTupleSpillGeometry {
	if !pgHashTupleSpillCostEnabled() {
		return pgHashTupleSpillGeometry{}
	}
	hash, ok := pgHashGeometry(in.innerRows, in.innerWidth, cp.workMem)
	if !ok {
		return pgHashTupleSpillGeometry{}
	}
	innerPages, innerOK := pgHashSpillPages(in.innerRows, in.innerWidth)
	outerPages, outerOK := pgHashSpillPages(in.outerRows, in.outerWidth)
	if !innerOK || !outerOK {
		return pgHashTupleSpillGeometry{hash: hash}
	}
	return pgHashTupleSpillGeometry{usePG: true, hash: hash, outerPages: outerPages, innerPages: innerPages}
}

// tracePGHashTupleGeometry records the two deliberately different sizing
// currencies for one offered Hash Join. It is diagnostic-only under the
// existing shaped-DP trace gate; the formatter has no path-selection role.
func tracePGHashTupleGeometry(outer, inner *Path, cp costParams) {
	if !pathTraceEnabled || outer == nil || inner == nil {
		return
	}
	mapSizing := hashsize.Choose(inner.Rows, pathNCols(inner), pathAvgVarBytes(inner), cp.workMem)
	in := hashJoinInputs{outerRows: outer.Rows, innerRows: inner.Rows, outerWidth: pathWidth(outer), innerWidth: pathWidth(inner)}
	decision := pgHashTupleSpillGeometryFor(in, cp)
	pgGeometry, pgOK := pgHashGeometry(in.innerRows, in.innerWidth, cp.workMem)
	currency := "map"
	if decision.usePG {
		currency = "pg"
	}
	pgTupleBytes, pgBuckets, pgBatches := "-", "-", "-"
	pgOuterPages, pgInnerPages := "-", "-"
	if pgOK {
		pgTupleBytes = fmt.Sprintf("%d", pgGeometry.tupleBytes)
		pgBuckets = fmt.Sprintf("%d", pgGeometry.numBuckets)
		pgBatches = fmt.Sprintf("%d", pgGeometry.numBatches)
	}
	if decision.usePG {
		pgOuterPages = fmt.Sprintf("%.0f", decision.outerPages)
		pgInnerPages = fmt.Sprintf("%.0f", decision.innerPages)
	}
	outerRelids, innerRelids := RelSet(0), RelSet(0)
	if outer.Rel != nil {
		outerRelids = outer.Rel.Relids
	}
	if inner.Rel != nil {
		innerRelids = inner.Rel.Relids
	}
	fmt.Fprintf(os.Stderr,
		"DPPGHASH outer=%s inner=%s mapentry=%.2f mapbuckets=%d mapbatches=%d mapspill=%t pgouterwidth=%d pginnerwidth=%d pgtuplebytes=%s pgbuckets=%s pgbatches=%s pgouterpages=%s pginnerpages=%s pgspill=%t currency=%s\n",
		relSetBits(outerRelids), relSetBits(innerRelids), mapSizing.EntryBytes,
		mapSizing.NBuckets, mapSizing.NBatch, mapSizing.NBatch > 1,
		pathWidth(outer), pathWidth(inner), pgTupleBytes, pgBuckets, pgBatches,
		pgOuterPages, pgInnerPages, decision.usePG && decision.hash.numBatches > 1, currency)
}
