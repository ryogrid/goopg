package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/goopg/goopg/internal/executor"
)

// pgConstraintEntry mirrors one row of PG18's pg_constraint (OID 2606) that a
// goopg initdb seeds into the heap: the two information_schema domain CHECK
// constraints created by information_schema.sql (M0133-S1).
//
// Until now goopg bootstrapped pg_constraint as an EMPTY placeholder — the
// runtime journals user-domain CHECKs via writeDomainCheckConstraintRow, but no
// bootstrap path ever wrote a row, so a hosted PG validated every domain as
// unconstrained (the ledgered "no pg_constraint index maintenance" residual).
// Seeding these two rows makes a hosted PG reject `-1::cardinal_number` and
// `'MAYBE'::yes_or_no` exactly as PG does.
type pgConstraintEntry struct {
	OID      uint32
	ConName  string
	ConTypID uint32 // contypid — the domain OID this check constrains
	ConBin   string // conbin — nodeToString(CheckExpr), captured from PG 18.3
}

// pgConstraintInitialEntries returns the two domain CHECK constraints PG18's
// information_schema.sql creates. Every field is measured against a fresh
// PG 18.3 (the OIDs come from the post-bootstrap counter, not a .dat file), and
// conbin is the verbatim nodeToString form so a hosted PG can actually parse
// and enforce it — unlike the runtime's raw-text adbin convention, which a
// standby cannot validate.
func pgConstraintInitialEntries() []pgConstraintEntry {
	return []pgConstraintEntry{
		{
			OID:      13288,
			ConName:  "cardinal_number_domain_check",
			ConTypID: 13287,
			ConBin:   `{OPEXPR :opno 525 :opfuncid 150 :opresulttype 16 :opretset false :opcollid 0 :inputcollid 0 :args ({COERCETODOMAINVALUE :typeId 23 :typeMod -1 :collation 0 :location -1} {CONST :consttype 23 :consttypmod -1 :constcollid 0 :constlen 4 :constbyval true :constisnull false :location -1 :constvalue 4 [ 0 0 0 0 0 0 0 0 ]}) :location -1}`,
		},
		{
			OID:      13301,
			ConName:  "yes_or_no_check",
			ConTypID: 13300,
			ConBin:   `{SCALARARRAYOPEXPR :opno 98 :opfuncid 67 :hashfuncid 0 :negfuncid 0 :useOr true :inputcollid 100 :args ({RELABELTYPE :arg {COERCETODOMAINVALUE :typeId 1043 :typeMod 7 :collation 100 :location -1} :resulttype 25 :resulttypmod -1 :resultcollid 100 :relabelformat 2 :location -1} {ARRAYCOERCEEXPR :arg {ARRAYEXPR :array_typeid 1015 :array_collid 100 :element_typeid 1043 :elements ({CONST :consttype 1043 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 7 [ 28 0 0 0 89 69 83 ]} {CONST :consttype 1043 :consttypmod -1 :constcollid 100 :constlen -1 :constbyval false :constisnull false :location -1 :constvalue 6 [ 24 0 0 0 78 79 ]}) :multidims false :list_start -1 :list_end -1 :location -1} :elemexpr {RELABELTYPE :arg {CASETESTEXPR :typeId 1043 :typeMod -1 :collation 0} :resulttype 25 :resulttypmod -1 :resultcollid 100 :relabelformat 2 :location -1} :resulttype 1009 :resulttypmod -1 :resultcollid 100 :coerceformat 2 :location -1}) :location -1}`,
		},
	}
}

// pgConstraintRow builds the 28-column Form_pg_constraint row in
// executor.PGConstraintColumnsPG18() order (the same layout the runtime's
// buildPGConstraintRowForDomainCheck journals). Value semantics mirror PG's
// CreateConstraintEntry for a validated, enforced, local domain CHECK: the
// FK-only char columns carry a space, the array columns are genuinely NULL,
// and conbin is a real pg_node_tree varlena (not the raw-text adbin deviation).
func pgConstraintRow(e pgConstraintEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),        // 1  oid
		executor.NewStringDatum(e.ConName),        // 2  conname
		executor.NewIntDatum(13273),               // 3  connamespace = information_schema
		executor.NewStringDatum("c"),              // 4  contype = check
		executor.NewBoolDatum(false),              // 5  condeferrable
		executor.NewBoolDatum(false),              // 6  condeferred
		executor.NewBoolDatum(true),               // 7  conenforced
		executor.NewBoolDatum(true),               // 8  convalidated
		executor.NewIntDatum(0),                   // 9  conrelid (no relation)
		executor.NewIntDatum(int64(e.ConTypID)),   // 10 contypid
		executor.NewIntDatum(0),                   // 11 conindid
		executor.NewIntDatum(0),                   // 12 conparentid
		executor.NewIntDatum(0),                   // 13 confrelid
		executor.NewStringDatum(" "),              // 14 confupdtype (space — non-FK)
		executor.NewStringDatum(" "),              // 15 confdeltype
		executor.NewStringDatum(" "),              // 16 confmatchtype
		executor.NewBoolDatum(true),               // 17 conislocal
		executor.NewIntDatum(0),                   // 18 coninhcount
		executor.NewBoolDatum(false),              // 19 connoinherit
		executor.NewBoolDatum(false),              // 20 conperiod
		executor.NullDatum,                        // 21 conkey
		executor.NullDatum,                        // 22 confkey
		executor.NullDatum,                        // 23 conpfeqop
		executor.NullDatum,                        // 24 conppeqop
		executor.NullDatum,                        // 25 conffeqop
		executor.NullDatum,                        // 26 confdelsetcols
		executor.NullDatum,                        // 27 conexclop
		pglzVarlenaDatum(e.ConBin),                // 28 conbin (real pg_node_tree)
	}
}

// bootstrapPgConstraintTuples writes the two information_schema domain CHECK
// rows to base/{1,5}/2606 (overwriting the empty placeholder) and returns a
// map from constraint OID to heapTID so the caller can build the three
// pg_constraint indexes (2665/2666/2667) over the same rows.
func bootstrapPgConstraintTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := executor.PGConstraintColumnsPG18()
	entries := pgConstraintInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgConstraintRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2606", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("pg_constraint heap: %w", err)
	}
	m := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		m[e.OID] = rawTIDs[i]
	}
	return m, nil
}

// bootstrapPgConstraintOidIndex writes pg_constraint_oid_index (OID 2667) — the
// UNIQUE PRIMARY KEY btree on oid — to base/{1,5}/2667. PG's CONSTROID syscache
// (SearchSysCache1(CONSTROID)) and get_domain_constraint_oid resolve through it.
func bootstrapPgConstraintOidIndex(dataDir string, tids map[uint32]heapTID) error {
	entries := pgConstraintInitialEntries()
	type indexed struct {
		oid   uint32
		block uint32
		off   uint16
	}
	items := make([]indexed, 0, len(entries))
	for _, e := range entries {
		tid, ok := tids[e.OID]
		if !ok {
			return fmt.Errorf("pg_constraint_oid_index: no heap TID for constraint OID %d", e.OID)
		}
		items = append(items, indexed{oid: e.OID, block: tid.Block, off: tid.Offset})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].oid < items[j].oid })

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidKey(it.block, it.off, it.oid)
	}
	file, err := pgBuildBtreeBulkLoad(tuples, 1)
	if err != nil {
		return fmt.Errorf("pg_constraint_oid_index bulk-load: %w", err)
	}
	return writeConstraintIndexFile(dataDir, 2667, file)
}

// bootstrapPgConstraintContypidIndex writes pg_constraint_contypid_index
// (OID 2666) — a btree on contypid — to base/{1,5}/2666. This is the index
// GetDomainConstraints scans (ConstraintTypidIndexId), so it is what makes a
// hosted PG actually SEE a domain's CHECK when it validates a value.
func bootstrapPgConstraintContypidIndex(dataDir string, tids map[uint32]heapTID) error {
	entries := pgConstraintInitialEntries()
	type indexed struct {
		typid uint32
		block uint32
		off   uint16
	}
	items := make([]indexed, 0, len(entries))
	for _, e := range entries {
		tid, ok := tids[e.OID]
		if !ok {
			return fmt.Errorf("pg_constraint_contypid_index: no heap TID for constraint OID %d", e.OID)
		}
		items = append(items, indexed{typid: e.ConTypID, block: tid.Block, off: tid.Offset})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].typid < items[j].typid })

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidKey(it.block, it.off, it.typid)
	}
	file, err := pgBuildBtreeBulkLoad(tuples, 1)
	if err != nil {
		return fmt.Errorf("pg_constraint_contypid_index bulk-load: %w", err)
	}
	return writeConstraintIndexFile(dataDir, 2666, file)
}

// bootstrapPgConstraintRelidTypidNameIndex writes
// pg_constraint_conrelid_contypid_conname_index (OID 2665) — a UNIQUE btree on
// (conrelid, contypid, conname) — to base/{1,5}/2665. PG resolves named
// constraints (e.g. `ALTER DOMAIN ... DROP CONSTRAINT`) through it.
func bootstrapPgConstraintRelidTypidNameIndex(dataDir string, tids map[uint32]heapTID) error {
	entries := pgConstraintInitialEntries()
	type indexed struct {
		relid uint32
		typid uint32
		name  string
		block uint32
		off   uint16
	}
	items := make([]indexed, 0, len(entries))
	for _, e := range entries {
		tid, ok := tids[e.OID]
		if !ok {
			return fmt.Errorf("pg_constraint_relid_typid_name_index: no heap TID for constraint OID %d", e.OID)
		}
		// conrelid is 0 for every domain CHECK (no relation).
		items = append(items, indexed{relid: 0, typid: e.ConTypID, name: e.ConName, block: tid.Block, off: tid.Offset})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.relid != b.relid {
			return a.relid < b.relid
		}
		if a.typid != b.typid {
			return a.typid < b.typid
		}
		return a.name < b.name
	})

	tuples := make([][]byte, len(items))
	for i, it := range items {
		tuples[i] = pgBuildIndexTupleOidOidNameKey(it.block, it.off, it.relid, it.typid, it.name)
	}
	file, err := pgBuildBtreeBulkLoadSized(tuples, 80, 3)
	if err != nil {
		return fmt.Errorf("pg_constraint_relid_typid_name_index bulk-load: %w", err)
	}
	return writeConstraintIndexFile(dataDir, 2665, file)
}

// writeConstraintIndexFile writes one pg_constraint btree index file to both
// base/1 and base/5 (the same two-DB bootstrap convention every other
// bootstrap index follows).
func writeConstraintIndexFile(dataDir string, oid uint32, file []byte) error {
	for _, dir := range []string{
		filepath.Join(dataDir, "base", "1"),
		filepath.Join(dataDir, "base", "5"),
	} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatUint(uint64(oid), 10)), file, 0o600); err != nil {
			return fmt.Errorf("write pg_constraint index %d in %s: %w", oid, dir, err)
		}
	}
	return nil
}
