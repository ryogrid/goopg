// pg_catalog.pg_aggregate SQL-query surface. The 161 BKI aggregate rows are
// written to a real heap file at initdb (bootstrapPgAggregateTuples, see
// pg_aggregate_bootstrap.go) purely for byte-level PG18 standby replication
// fidelity — but, unlike pg_type/pg_attribute, that heap is never wired to
// goopg's own SQL engine as a scannable relation (mirrors the earlier
// pg_index gap: pg_index is ALSO heap-written for standby fidelity, but
// SQL-facing queries are served by a Virtual table computed from the live
// index registry — catalog.go's `Schema: "pg_catalog", Name: "pg_index",
// Virtual: true`). Before this, `SELECT * FROM pg_aggregate` failed with
// "relation does not exist" for every session, built-in aggregates included,
// which silently made pg_dump's dumpAgg (`FROM pg_aggregate a, pg_proc p`)
// impossible for ANY aggregate. DU-002 slice 405.
package initdb

import (
	"fmt"
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// registerPgAggregateView installs `pg_catalog.pg_aggregate` as a Virtual
// table combining the 161 built-in (BKI) aggregate rows with every
// CREATE AGGREGATE registered in the live catalog.
func registerPgAggregateView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema:  "pg_catalog",
		Name:    "pg_aggregate",
		Columns: pgAggregateColDefs(),
		OID:     catalog.AggregateRelationId,
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		bki := pgAggregateInitialEntries()
		rows := make([][]string, 0, len(bki)+16)
		for _, e := range bki {
			rows = append(rows, []string{
				fmt.Sprintf("%d", e.AggFnOID),
				string(e.AggKind),
				fmt.Sprintf("%d", e.AggNumDirectArgs),
				aggBuiltinFuncName(e.AggTransFn),
				aggBuiltinFuncName(e.AggFinalFn),
				aggBuiltinFuncName(e.AggCombineFn),
				aggBuiltinFuncName(e.AggSerialFn),
				aggBuiltinFuncName(e.AggDeserialFn),
				aggBuiltinFuncName(e.AggMTransFn),
				aggBuiltinFuncName(e.AggMInvTransFn),
				aggBuiltinFuncName(e.AggMFinalFn),
				boolToTF(e.AggFinalExtra),
				boolToTF(e.AggMFinalExtra),
				string(e.AggFinalModify),
				string(e.AggMFinalModify),
				fmt.Sprintf("%d", e.AggSortOp),
				fmt.Sprintf("%d", e.AggTransType),
				fmt.Sprintf("%d", e.AggTransSpace),
				fmt.Sprintf("%d", e.AggMTransType),
				fmt.Sprintf("%d", e.AggMTransSpace),
				nullableAggText(e.AggInitVal),
				nullableAggText(e.AggMInitVal),
			})
		}
		// Append user-defined aggregates (CREATE AGGREGATE). aggkind is always
		// 'n' (normal) and the moving-aggregate columns are always zero/default
		// -- goopg parses but discards MSFUNC/MINVFUNC/... (parseAggregateOptions).
		for _, agg := range cat.ListUserAggregates() {
			rows = append(rows, []string{
				fmt.Sprintf("%d", agg.OID),
				"n",
				"0",
				aggFuncNameOrDash(agg.SFunc),
				aggFuncNameOrDash(agg.FinalFunc),
				aggFuncNameOrDash(agg.CombineFunc),
				"-", // aggserialfn (not modeled)
				"-", // aggdeserialfn (not modeled)
				"-", // aggmtransfn (moving-aggregate not modeled)
				"-", // aggminvtransfn (moving-aggregate not modeled)
				"-", // aggmfinalfn (moving-aggregate not modeled)
				"f", // aggfinalextra
				"f", // aggmfinalextra
				executor.AggFinalModifyChar(agg.FinalFuncModify),
				"r", // aggmfinalmodify (moving-aggregate not modeled)
				"0", // aggsortop
				fmt.Sprintf("%d", executor.AggregateTypeOID(agg.SType)),
				"0", // aggtransspace
				"0", // aggmtranstype
				"0", // aggmtransspace
				nullableAggText(agg.InitCond),
				catalog.VirtualNull, // aggminitval (NULL; no moving-aggregate initcond parsed)
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}

// aggFuncNameOrDash renders a CREATE AGGREGATE function-name reference
// (SFUNC/FINALFUNC/COMBINEFUNC) the way real PostgreSQL's regprocout renders
// a pg_aggregate.aggtransfn/aggfinalfn/aggcombinefn value: the bare function
// name (unqualified — every curated builtin lives in pg_catalog, which is
// always on the implicit search_path), or "-" for InvalidOid/unset (mirrors
// the ::regproc cast's OID-0 handling in executor/expr.go). Unlike aggfnoid
// (kept numeric so `a.aggfnoid = p.oid` type-checks/joins, DU-002 slice 405),
// these columns are never joined on — dumpAgg reads them as opaque text, so
// storing the resolved name directly (the same convention pg_conversion.conproc
// already uses) sidesteps goopg not yet resolving a regproc OID back to a name
// at generic column-output time.
func aggFuncNameOrDash(name string) string {
	if name == "" {
		return "-"
	}
	return name
}

// pgProcOIDNameIndex is a reverse OID→proname lookup over the full 3397-row
// generated pg_proc.dat (pgProcAllEntries), built lazily and cached: the
// forward table (catalog.LookupBuiltinProc) only curates the handful of
// builtins referenced by name in fixtures, but aggBuiltinFuncName needs
// every built-in aggregate support function's OID resolved (161 distinct
// transfn/finalfn/combinefn/... references across the BKI pg_aggregate
// rows), so it is cheaper and more accurate to index the already-generated
// full table than to hand-curate a second one. DU-002 slice 405 resume (b).
var (
	pgProcOIDNameIndexOnce sync.Once
	pgProcOIDNameIndex     map[uint32]string
)

func pgProcNameForOID(oid uint32) (string, bool) {
	pgProcOIDNameIndexOnce.Do(func() {
		entries := pgProcAllEntries()
		pgProcOIDNameIndex = make(map[uint32]string, len(entries))
		for _, e := range entries {
			pgProcOIDNameIndex[e.OID] = e.Name
		}
	})
	name, ok := pgProcOIDNameIndex[oid]
	return name, ok
}

// aggBuiltinFuncName resolves a BKI pg_aggregate row's aggtransfn/aggfinalfn/
// aggcombinefn/aggserialfn/aggdeserialfn/aggmtransfn/aggminvtransfn/
// aggmfinalfn OID to its pg_proc.proname, matching how real PostgreSQL's
// regprocout renders a regproc column on a direct query (e.g. `SELECT
// aggtransfn FROM pg_aggregate`) -- not just the ::regproc cast path already
// handled in executor/expr.go. InvalidOid (0, the moving-aggregate columns
// of a plain non-window aggregate) renders "-", mirroring aggFuncNameOrDash
// above and PG's regproc.c. Every non-zero OID pg_aggregate.dat references
// is guaranteed present in pgProcAllEntries (both generated from the same
// PG18 .dat files); an unresolved OID falls back to its numeric text rather
// than panicking, a defensive guard rather than an expected path.
func aggBuiltinFuncName(oid uint32) string {
	if oid == 0 {
		return "-"
	}
	if name, ok := pgProcNameForOID(oid); ok {
		return name
	}
	return fmt.Sprintf("%d", oid)
}

// nullableAggText maps an empty agginitval/aggminitval to the catalog.VirtualNull
// sentinel. Unlike most other nullable columns in this package's VirtualRows
// (proconfig, probin, ...), a bare "" is NOT a safe stand-in for NULL here:
// agginitval is declared type "text", the exact case catalog.VirtualNull's doc
// comment calls out as needing the sentinel, since an aggregate could
// legitimately declare an empty-string INITCOND. Confirmed empirically: a bare
// "" rendered `MINITCOND = ''` in a real pg_dump instead of omitting the
// clause. DU-002 slice 405.
func nullableAggText(s string) string {
	if s == "" {
		return catalog.VirtualNull
	}
	return s
}

// boolToTF renders a Go bool as PG's "t"/"f" boolean text representation.
func boolToTF(b bool) string {
	if b {
		return "t"
	}
	return "f"
}
