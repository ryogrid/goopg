package executor

// B2.1b (docs/design/wal-pg-identical-stream/02d §1): domain CHECK
// constraints journal as real pg_constraint heap rows (contype='c',
// contypid=<domain OID>) so the startup reload reconstructs them from the
// heap instead of the retired CreateDomain WAL record (kind 119). The
// pg_constraint VIEW stays virtual (registry-backed) — this heap is a
// write+reload surface only, exactly like pg_class (see
// goopg_pg_class_virtual_pg_attribute_heap).
//
// Scope notes (ledgered residuals):
//   - No pg_constraint index maintenance (2664-2667 stay bootstrap-empty):
//     a real PG standby loads domain constraints via
//     pg_constraint_contypid_index (2666) and so sees NONE — it accepts
//     values a goopg primary would reject. Rides the full pg_constraint
//     conversion (B3).
//   - conbin carries the raw CHECK expression text, the same deviation
//     convention as pg_type.typdefaultbin / pg_attrdef.adbin.
//   - Table constraints stay registry-only until B3.

import (
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgConstraintRelOID is pg_constraint's relation OID.
const pgConstraintRelOID = 2606

// PGTypeColumnsPG18 exports the 32-column pg_type layout for the initdb
// reload descriptor (B2.1b — twin of pgTypeColumnsPG18, which stays
// unexported beside its builders in pg18_user_catalog_rows.go).
func PGTypeColumnsPG18() []catalog.Column { return pgTypeColumnsPG18() }

// PGConstraintColumnsPG18 mirrors FormData_pg_constraint
// (postgres/src/include/catalog/pg_constraint.h): the 28-column PG18 layout.
// Exported for the initdb reload descriptor.
func PGConstraintColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "conname", Type: catalog.Type{Name: "name"}},
		{Name: "connamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "contype", Type: catalog.Type{Name: "char"}},
		{Name: "condeferrable", Type: catalog.Type{Name: "bool"}},
		{Name: "condeferred", Type: catalog.Type{Name: "bool"}},
		{Name: "conenforced", Type: catalog.Type{Name: "bool"}},
		{Name: "convalidated", Type: catalog.Type{Name: "bool"}},
		{Name: "conrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "contypid", Type: catalog.Type{Name: "oid"}},
		{Name: "conindid", Type: catalog.Type{Name: "oid"}},
		{Name: "conparentid", Type: catalog.Type{Name: "oid"}},
		{Name: "confrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "confupdtype", Type: catalog.Type{Name: "char"}},
		{Name: "confdeltype", Type: catalog.Type{Name: "char"}},
		{Name: "confmatchtype", Type: catalog.Type{Name: "char"}},
		{Name: "conislocal", Type: catalog.Type{Name: "bool"}},
		{Name: "coninhcount", Type: catalog.Type{Name: "int2"}},
		{Name: "connoinherit", Type: catalog.Type{Name: "bool"}},
		{Name: "conperiod", Type: catalog.Type{Name: "bool"}},
		{Name: "conkey", Type: catalog.Type{Name: "int2[]"}},
		{Name: "confkey", Type: catalog.Type{Name: "int2[]"}},
		{Name: "conpfeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conppeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conffeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "confdelsetcols", Type: catalog.Type{Name: "int2[]"}},
		{Name: "conexclop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conbin", Type: catalog.Type{Name: "pg_node_tree"}},
	}
}

// buildPGConstraintRowForDomainCheck builds the pg_constraint row for one
// domain CHECK constraint. Value semantics mirror PG's CreateConstraintEntry
// for a validated, enforced, local domain check: FK-only char columns carry
// the zero char, array columns are genuinely NULL.
func buildPGConstraintRowForDomainCheck(d *catalog.Domain, chk catalog.DomainCheck) Row {
	return Row{
		NewIntDatum(int64(chk.OID)),                    // 1  oid
		NewStringDatum(chk.Name),                       // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("c"),                            // 4  contype
		NewBoolDatum(false),                            // 5  condeferrable
		NewBoolDatum(false),                            // 6  condeferred
		NewBoolDatum(true),                             // 7  conenforced
		NewBoolDatum(true),                             // 8  convalidated
		NewIntDatum(0),                                 // 9  conrelid
		NewIntDatum(int64(d.OID)),                      // 10 contypid
		NewIntDatum(0),                                 // 11 conindid
		NewIntDatum(0),                                 // 12 conparentid
		NewIntDatum(0),                                 // 13 confrelid
		NewStringDatum(""),                             // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                             // 15 confdeltype
		NewStringDatum(""),                             // 16 confmatchtype
		NewBoolDatum(true),                             // 17 conislocal
		NewIntDatum(0),                                 // 18 coninhcount
		NewBoolDatum(false),                            // 19 connoinherit
		NewBoolDatum(false),                            // 20 conperiod
		NullDatum,                                      // 21 conkey
		NullDatum,                                      // 22 confkey
		NullDatum,                                      // 23 conpfeqop
		NullDatum,                                      // 24 conppeqop
		NullDatum,                                      // 25 conffeqop
		NullDatum,                                      // 26 confdelsetcols
		NullDatum,                                      // 27 conexclop
		NewStringDatum(chk.Expr),                       // 28 conbin (raw expr text — adbin convention)
	}
}

func pgConstraintRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgConstraintRelOID,
		Fork:   storage.MainFork,
	}
}

// pgConstraintTableRel is pgConstraintRel's per-database twin, for rows that
// hang off a TABLE rather than a domain.
//
// R126: domain CHECK rows are pinned to DefaultDBOid (pgConstraintRel above)
// plus an explicit mirror, because a domain's own pg_type row is. A foreign
// key belongs to a table, and a table's pg_class row routes through
// tableCatalogHeapDBOid — "rows written to a distinct database's own catalog
// heap live ONLY there" (tableCatalogDBOids' header). Writing an FK to
// DefaultDBOid would leave it invisible to the per-DB reload for every table
// outside `postgres`, which is where the TPC-H corpus lives.
//
// Consequence, stated because the two are easy to confuse: the FK reload pass
// and the domain CHECK reload pass read DIFFERENT databases by design
// (DefaultDBOid + the per-DB sweep vs cat.DBOID()).
func pgConstraintTableRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: pgConstraintRelOID,
		Fork:   storage.MainFork,
	}
}

// int2ArrayDatum encodes attnums as a PG-native int2[] ArrayType blob wrapped
// in a KindBytes datum, or NullDatum when empty.
//
// R126. PGConstraintColumnsPG18 declares conkey/confkey as {Name:"int2[]"}
// with IsArray FALSE, so encodeValuePGCtx takes the `case "int2[]"` arm
// (codec.go:962), whose KindBytes passthrough is the documented way to write a
// non-empty array — the same mechanism the pg_proc seeder uses. Anything that
// is NOT KindBytes there silently becomes emptyArrayTypeBytes(21), i.e. an
// EMPTY array, which would make keysCovering decline and lose the whole point.
//
// The blob itself comes from encodeArrayValuePGCtx via an element-named
// IsArray type, NOT from a hand-rolled builder: that function carries the
// construct_md_array trailing-pad fidelity fix (codec_array.go:97-101) which a
// re-transcription would drop and which pg_column_size and pg_amcheck can see.
//
// NOTE: the columns are deliberately NOT redeclared as {Name:"int2",
// IsArray:true}. PhysicalTypeIsVarlena switches on Name with no IsArray arm
// (physical_align.go:85-107), so that spelling reports int2 as fixed-width and
// HEAP_HASVARWIDTH would go UNSET on a row whose only varlena is a non-null
// conkey — tripping PG18's nocachegetattr fast-path walker (the hazard
// codec.go:1550-1557 names). goopg's own encode and decode both branch on
// IsArray, so they would stay symmetric and a round-trip test would pass while
// the on-disk infomask was wrong.
func int2ArrayDatum(attnums []int16) (Datum, error) {
	if len(attnums) == 0 {
		return NullDatum, nil
	}
	parts := make([]string, len(attnums))
	for i, n := range attnums {
		parts[i] = strconv.FormatInt(int64(n), 10)
	}
	blob, err := encodeArrayValuePGCtx(
		catalog.Type{Name: "int2", IsArray: true},
		NewStringDatum("{"+strings.Join(parts, ",")+"}"), nil, 0)
	if err != nil {
		return NullDatum, err
	}
	return NewBytesDatum(blob), nil
}

// buildPGConstraintRowForForeignKey builds the contype='f' pg_constraint row
// for one foreign key. Sibling of buildPGConstraintRowForDomainCheck; value
// semantics mirror PG's CreateConstraintEntry for a foreign key.
//
// conbin is NULL, not "" — PG stores no expression for an FK, and an empty
// TEXT would be a non-null varlena that sets HEAP_HASVARWIDTH unconditionally,
// masking exactly the infomask defect the conkey encoding above is written to
// avoid.
func buildPGConstraintRowForForeignKey(fk catalog.ForeignKey, conrelid, confrelid uint32, conkey, confkey, confdelsetcols []int16) (Row, error) {
	conkeyDatum, err := int2ArrayDatum(conkey)
	if err != nil {
		return nil, err
	}
	confkeyDatum, err := int2ArrayDatum(confkey)
	if err != nil {
		return nil, err
	}
	// confdelsetcols (PG15): NULL when the ON DELETE SET action covers the
	// whole key, matching PG — decompile_column_index_array emits the
	// ` (col, …)` suffix only when it is non-null.
	setColsDatum, err := int2ArrayDatum(confdelsetcols)
	if err != nil {
		return nil, err
	}
	// NOT ENFORCED implies not validated, mirroring PG's processCASbits and
	// the synthesised view's own projection (catalog.go:7305).
	validated := !fk.NotValid && !fk.NotEnforced
	matchtype := "s" // MATCH SIMPLE
	if fk.MatchFull {
		matchtype = "f"
	}
	return Row{
		NewIntDatum(int64(fk.OID)),                        // 1  oid
		NewStringDatum(fk.Name),                           // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)),    // 3  connamespace
		NewStringDatum("f"),                               // 4  contype
		NewBoolDatum(fk.Deferrable),                       // 5  condeferrable
		NewBoolDatum(fk.InitiallyDeferred),                // 6  condeferred
		NewBoolDatum(!fk.NotEnforced),                     // 7  conenforced
		NewBoolDatum(validated),                           // 8  convalidated
		NewIntDatum(int64(conrelid)),                      // 9  conrelid
		NewIntDatum(0),                                    // 10 contypid (not a domain constraint)
		NewIntDatum(0),                                    // 11 conindid
		NewIntDatum(0),                                    // 12 conparentid
		NewIntDatum(int64(confrelid)),                     // 13 confrelid
		NewStringDatum(string(catalog.FKActionChar(fk.OnUpdate))), // 14 confupdtype
		NewStringDatum(string(catalog.FKActionChar(fk.OnDelete))), // 15 confdeltype
		NewStringDatum(matchtype),                         // 16 confmatchtype
		NewBoolDatum(true),                                // 17 conislocal
		NewIntDatum(0),                                    // 18 coninhcount
		NewBoolDatum(false),                               // 19 connoinherit
		NewBoolDatum(false),                               // 20 conperiod
		conkeyDatum,                                       // 21 conkey
		confkeyDatum,                                      // 22 confkey
		NullDatum,                                         // 23 conpfeqop — see note
		NullDatum,                                         // 24 conppeqop
		NullDatum,                                         // 25 conffeqop
		setColsDatum,                                      // 26 confdelsetcols
		NullDatum,                                         // 27 conexclop
		NullDatum,                                         // 28 conbin (NULL for an FK, never "")
	}, nil
}

// buildPGConstraintRowForTableCheck builds the contype='c' pg_constraint row
// for one table-level CHECK constraint (M0143-0003b). Field values mirror the
// synthesised view's own projection (catalog.go's
// InMemory.PGConstraintRowsForDBOid, table-CHECK block) exactly, so a restart
// does not change what the row — and therefore pg_dump — reports.
//
// conkey stays NULL, not the referenced columns: real PG's CHECK constraints
// never populate conkey (only NOT NULL/PK/UNIQUE/FK do), and the synthesised
// view leaves it unset too — see the comment at int2ArrayDatum above for why
// getting this wrong would be an infomask hazard, not just a cosmetic gap.
func buildPGConstraintRowForTableCheck(tbl *catalog.Table, nc catalog.NamedCheckConstraint) Row {
	// NOT ENFORCED implies not validated (processCASbits), same rule as the
	// FK builder and the synthesised view's own `nc.NotValid || nc.NotEnforced`.
	convalidated := !nc.NotValid && !nc.NotEnforced
	return Row{
		NewIntDatum(int64(nc.OID)),                     // 1  oid
		NewStringDatum(nc.Name),                        // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("c"),                            // 4  contype
		NewBoolDatum(false),                            // 5  condeferrable
		NewBoolDatum(false),                            // 6  condeferred
		NewBoolDatum(!nc.NotEnforced),                  // 7  conenforced
		NewBoolDatum(convalidated),                     // 8  convalidated
		NewIntDatum(int64(tbl.OID)),                    // 9  conrelid
		NewIntDatum(0),                                 // 10 contypid (not a domain constraint)
		NewIntDatum(0),                                 // 11 conindid
		NewIntDatum(0),                                 // 12 conparentid
		NewIntDatum(0),                                 // 13 confrelid
		NewStringDatum(""),                             // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                             // 15 confdeltype
		NewStringDatum(""),                             // 16 confmatchtype
		NewBoolDatum(nc.IsLocal),                        // 17 conislocal
		NewIntDatum(int64(nc.InhCount)),                 // 18 coninhcount
		NewBoolDatum(nc.NoInherit),                      // 19 connoinherit
		NewBoolDatum(false),                             // 20 conperiod
		NullDatum,                                       // 21 conkey — see note above
		NullDatum,                                       // 22 confkey
		NullDatum,                                       // 23 conpfeqop
		NullDatum,                                       // 24 conppeqop
		NullDatum,                                       // 25 conffeqop
		NullDatum,                                       // 26 confdelsetcols
		NullDatum,                                       // 27 conexclop
		NewStringDatum(nc.Expr),                         // 28 conbin (raw expr text — adbin convention)
	}
}

// buildPGConstraintRowForNotNull builds the contype='n' pg_constraint row for
// one named NOT NULL constraint (M0143-0003d). Field values mirror the
// synthesised view's own projection (catalog.go's InMemory.
// PGConstraintRowsForDBOid, NOT NULL block): conenforced is always true (PG
// has no NOT ENFORCED spelling for a NOT NULL constraint, so
// NamedNotNullConstraint carries no NotEnforced field to begin with),
// convalidated is the inverse of NotValid, and conkey carries the single
// column ordinal — unlike CHECK, a NOT NULL constraint's conkey is NOT NULL
// in real PG (ruleutils.c's print_notnull path reads it to find the column).
func buildPGConstraintRowForNotNull(tbl *catalog.Table, nc catalog.NamedNotNullConstraint) (Row, error) {
	var colOrd int16
	for i, col := range tbl.Columns {
		if strings.EqualFold(col.Name, nc.ColName) {
			colOrd = int16(i + 1)
			break
		}
	}
	conkeyDatum := NullDatum
	if colOrd > 0 {
		var err error
		conkeyDatum, err = int2ArrayDatum([]int16{colOrd})
		if err != nil {
			return nil, err
		}
	}
	return Row{
		NewIntDatum(int64(nc.OID)),                     // 1  oid
		NewStringDatum(nc.Name),                        // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("n"),                            // 4  contype
		NewBoolDatum(false),                            // 5  condeferrable
		NewBoolDatum(false),                            // 6  condeferred
		NewBoolDatum(true),                              // 7  conenforced — always true (no NOT ENFORCED for NOT NULL)
		NewBoolDatum(!nc.NotValid),                      // 8  convalidated
		NewIntDatum(int64(tbl.OID)),                     // 9  conrelid
		NewIntDatum(0),                                  // 10 contypid (not a domain constraint)
		NewIntDatum(0),                                  // 11 conindid
		NewIntDatum(0),                                  // 12 conparentid
		NewIntDatum(0),                                  // 13 confrelid
		NewStringDatum(""),                              // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                              // 15 confdeltype
		NewStringDatum(""),                              // 16 confmatchtype
		NewBoolDatum(nc.IsLocal),                         // 17 conislocal
		NewIntDatum(int64(nc.InhCount)),                  // 18 coninhcount
		NewBoolDatum(nc.NoInherit),                       // 19 connoinherit
		NewBoolDatum(false),                              // 20 conperiod
		conkeyDatum,                                      // 21 conkey — the one NOT NULL column
		NullDatum,                                        // 22 confkey
		NullDatum,                                        // 23 conpfeqop
		NullDatum,                                        // 24 conppeqop
		NullDatum,                                        // 25 conffeqop
		NullDatum,                                        // 26 confdelsetcols
		NullDatum,                                        // 27 conexclop
		NullDatum,                                        // 28 conbin — NOT NULL has no expression
	}, nil
}

// buildPGConstraintRowForUnique builds the contype='u' pg_constraint row for
// one UNIQUE (non-PRIMARY-KEY) constraint-backed index (M0143-0003c). Unlike
// CHECK/NOT NULL, the constraint object here IS the index — there is no
// separate NamedUniqueConstraint store — so the row's identity fields come
// straight off catalog.Index: conname=idx.Name (real PG names the index after
// the constraint by default), condeferrable/condeferred=idx.Deferrable/
// InitiallyDeferred, and — the field this task exists to persist —
// conindid=idx.OID. That conindid link is exactly how real PG tells an
// ADD CONSTRAINT ... UNIQUE index apart from a bare CREATE UNIQUE INDEX on
// reload (indisunique is identical for both; only the constraint pointing
// back at the index distinguishes them). Field values otherwise mirror the
// synthesised view's own projection (catalog.go's InMemory.
// PGConstraintRowsForDBOid, contype='u'/'p'/'x' block): conenforced/
// convalidated/conislocal are hardcoded true, coninhcount/connoinherit
// hardcoded 0/false — the view tracks no inheritance-locality state for
// index-backed constraints the way NamedCheckConstraint/
// NamedNotNullConstraint do, so a new heap row must not invent any either.
//
// conkey carries every key column's 1-based ordinal (like NOT NULL, unlike
// CHECK) — real PG always populates it for 'u'/'p'/'x' constraints. It comes
// back NULL only if a key column can't be resolved to a plain table column
// (an expression key, which ADD CONSTRAINT ... UNIQUE cannot produce; the
// guard exists for defensive symmetry with buildPGConstraintRowForNotNull,
// not a documented PG scenario).
func buildPGConstraintRowForUnique(tbl *catalog.Table, idx *catalog.Index) (Row, error) {
	conkeyDatum := NullDatum
	if len(idx.Columns) > 0 {
		attnums := make([]int16, 0, len(idx.Columns))
		resolved := true
		for _, colName := range idx.Columns {
			if colName == "" { // expression key column — no attnum to record
				resolved = false
				break
			}
			ord, found := int16(0), false
			for i, col := range tbl.Columns {
				if strings.EqualFold(col.Name, colName) {
					ord = int16(i + 1)
					found = true
					break
				}
			}
			if !found {
				resolved = false
				break
			}
			attnums = append(attnums, ord)
		}
		if resolved {
			var err error
			conkeyDatum, err = int2ArrayDatum(attnums)
			if err != nil {
				return nil, err
			}
		}
	}
	return Row{
		NewIntDatum(int64(idx.OID)),                    // 1  oid
		NewStringDatum(idx.Name),                       // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("u"),                            // 4  contype
		NewBoolDatum(idx.Deferrable),                   // 5  condeferrable
		NewBoolDatum(idx.InitiallyDeferred),            // 6  condeferred
		NewBoolDatum(true),                             // 7  conenforced
		NewBoolDatum(true),                             // 8  convalidated
		NewIntDatum(int64(tbl.OID)),                    // 9  conrelid
		NewIntDatum(0),                                 // 10 contypid (not a domain constraint)
		NewIntDatum(int64(idx.OID)),                    // 11 conindid — the durable UNIQUE-constraint signal
		NewIntDatum(0),                                 // 12 conparentid
		NewIntDatum(0),                                 // 13 confrelid
		NewStringDatum(""),                             // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                             // 15 confdeltype
		NewStringDatum(""),                             // 16 confmatchtype
		NewBoolDatum(true),                             // 17 conislocal
		NewIntDatum(0),                                 // 18 coninhcount
		NewBoolDatum(false),                            // 19 connoinherit
		NewBoolDatum(false),                            // 20 conperiod
		conkeyDatum,                                    // 21 conkey — the constraint's key columns
		NullDatum,                                      // 22 confkey
		NullDatum,                                      // 23 conpfeqop
		NullDatum,                                      // 24 conppeqop
		NullDatum,                                      // 25 conffeqop
		NullDatum,                                      // 26 confdelsetcols
		NullDatum,                                      // 27 conexclop
		NullDatum,                                      // 28 conbin — no expression for a UNIQUE constraint
	}, nil
}

// buildPGConstraintRowForExclude builds the contype='x' pg_constraint row for
// one EXCLUDE-constraint-backed index (M0143-0003e). Sibling of
// buildPGConstraintRowForUnique — same conindid-keyed identity (the
// constraint object IS the index) and the same field values otherwise
// (synthesised view's contype='u'/'p'/'x' block), with two differences:
//
//   - conbin carries idx.ExclusionOp ("=" or "&&") as raw text, the same
//     smuggling convention this file's header comment documents for
//     CHECK/domain adbin — real PG never populates conbin for an EXCLUDE
//     constraint (pg_get_constraintdef decompiles conkey+conexclop instead),
//     so the column is otherwise unused and safe to repurpose. Without this,
//     reload would restore idx.IsExclusion but leave idx.ExclusionOp empty,
//     which checkExclusionConstraintsForInsert's switch (no default case)
//     silently treats as "no enforcement" — restoring the metadata but not
//     the runtime check, worse than an obviously-missing feature.
//   - the gate for which indexes get a row is idx.IsExclusion alone, not
//     idx.Unique && idx.IsConstraint: the non-btree-equality EXCLUDE path
//     (createExclusionIndexStub) never sets IsConstraint or Unique, yet real
//     PG always creates a pg_constraint row for any EXCLUDE constraint (see
//     the call site in syncTableToCatalogHeap for the full reasoning).
func buildPGConstraintRowForExclude(tbl *catalog.Table, idx *catalog.Index) (Row, error) {
	conkeyDatum := NullDatum
	if len(idx.Columns) > 0 {
		attnums := make([]int16, 0, len(idx.Columns))
		resolved := true
		for _, colName := range idx.Columns {
			if colName == "" { // expression key column — no attnum to record
				resolved = false
				break
			}
			ord, found := int16(0), false
			for i, col := range tbl.Columns {
				if strings.EqualFold(col.Name, colName) {
					ord = int16(i + 1)
					found = true
					break
				}
			}
			if !found {
				resolved = false
				break
			}
			attnums = append(attnums, ord)
		}
		if resolved {
			var err error
			conkeyDatum, err = int2ArrayDatum(attnums)
			if err != nil {
				return nil, err
			}
		}
	}
	return Row{
		NewIntDatum(int64(idx.OID)),                    // 1  oid
		NewStringDatum(idx.Name),                       // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("x"),                            // 4  contype
		NewBoolDatum(idx.Deferrable),                   // 5  condeferrable
		NewBoolDatum(idx.InitiallyDeferred),            // 6  condeferred
		NewBoolDatum(true),                             // 7  conenforced
		NewBoolDatum(true),                             // 8  convalidated
		NewIntDatum(int64(tbl.OID)),                    // 9  conrelid
		NewIntDatum(0),                                 // 10 contypid (not a domain constraint)
		NewIntDatum(int64(idx.OID)),                    // 11 conindid — the durable EXCLUDE-constraint signal
		NewIntDatum(0),                                 // 12 conparentid
		NewIntDatum(0),                                 // 13 confrelid
		NewStringDatum(""),                             // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                             // 15 confdeltype
		NewStringDatum(""),                             // 16 confmatchtype
		NewBoolDatum(true),                             // 17 conislocal
		NewIntDatum(0),                                 // 18 coninhcount
		NewBoolDatum(false),                            // 19 connoinherit
		NewBoolDatum(false),                            // 20 conperiod
		conkeyDatum,                                    // 21 conkey — the constraint's key columns
		NullDatum,                                      // 22 confkey
		NullDatum,                                      // 23 conpfeqop
		NullDatum,                                      // 24 conppeqop
		NullDatum,                                      // 25 conffeqop
		NullDatum,                                      // 26 confdelsetcols
		NullDatum,                                      // 27 conexclop — see note above (not oid[]-encoded)
		NewStringDatum(idx.ExclusionOp),                // 28 conbin — repurposed to carry ExclusionOp text
	}, nil
}

// writeExclusionConstraintRow journals one EXCLUDE-constraint-backed index as
// a pg_constraint heap INSERT into the TABLE's database (same per-DB routing
// as writeUniqueConstraintRow, M0143-0003e).
func writeExclusionConstraintRow(ctx *Context, tbl *catalog.Table, idx *catalog.Index) error {
	row, err := buildPGConstraintRowForExclude(tbl, idx)
	if err != nil {
		return err
	}
	_, err = writeHeapRowCanonical(ctx, pgConstraintTableRel(ctx), PGConstraintColumnsPG18(), row)
	return err
}

// stampExclusionConstraintRows stamps xmax on every contype='x' TABLE-level
// row (conrelid=relOID) in the given database's pg_constraint heap. Mirrors
// stampUniqueConstraintRows exactly (M0143-0003e).
func stampExclusionConstraintRows(ctx *Context, dbOid, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgConstraintRelOID, Fork: storage.MainFork}
	cols := PGConstraintColumnsPG18()
	stampCatalogRowsTuple(ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "x" && uint32(decoded[8].Int) == relOID
	})
}

// writeUniqueConstraintRow journals one UNIQUE-constraint-backed index as a
// pg_constraint heap INSERT into the TABLE's database (same per-DB routing as
// writeCheckConstraintRow/writeNotNullConstraintRow, M0143-0003c).
func writeUniqueConstraintRow(ctx *Context, tbl *catalog.Table, idx *catalog.Index) error {
	row, err := buildPGConstraintRowForUnique(tbl, idx)
	if err != nil {
		return err
	}
	_, err = writeHeapRowCanonical(ctx, pgConstraintTableRel(ctx), PGConstraintColumnsPG18(), row)
	return err
}

// stampUniqueConstraintRows stamps xmax on every contype='u' TABLE-level row
// (conrelid=relOID) in the given database's pg_constraint heap. Mirrors
// stampCheckConstraintRows/stampNotNullConstraintRows exactly (M0143-0003c).
func stampUniqueConstraintRows(ctx *Context, dbOid, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgConstraintRelOID, Fork: storage.MainFork}
	cols := PGConstraintColumnsPG18()
	stampCatalogRowsTuple(ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "u" && uint32(decoded[8].Int) == relOID
	})
}

// writeNotNullConstraintRow journals one named NOT NULL constraint as a
// pg_constraint heap INSERT into the TABLE's database (same per-DB routing as
// writeCheckConstraintRow, M0143-0003d).
func writeNotNullConstraintRow(ctx *Context, tbl *catalog.Table, nc catalog.NamedNotNullConstraint) error {
	row, err := buildPGConstraintRowForNotNull(tbl, nc)
	if err != nil {
		return err
	}
	_, err = writeHeapRowCanonical(ctx, pgConstraintTableRel(ctx), PGConstraintColumnsPG18(), row)
	return err
}

// stampNotNullConstraintRows stamps xmax on every contype='n' TABLE-level row
// (conrelid=relOID) in the given database's pg_constraint heap. Mirrors
// stampCheckConstraintRows exactly (M0143-0003d) — see its comment for why the
// row must be decoded via the descriptor rather than matched at a fixed byte
// offset.
func stampNotNullConstraintRows(ctx *Context, dbOid, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgConstraintRelOID, Fork: storage.MainFork}
	cols := PGConstraintColumnsPG18()
	stampCatalogRowsTuple(ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "n" && uint32(decoded[8].Int) == relOID
	})
}

// writeCheckConstraintRow journals one table-level CHECK constraint as a
// pg_constraint heap INSERT into the TABLE's database (see
// pgConstraintTableRel — same per-DB routing writeForeignKeyConstraintRow
// uses, R126's precedent, M0143-0003b).
func writeCheckConstraintRow(ctx *Context, tbl *catalog.Table, nc catalog.NamedCheckConstraint) error {
	_, err := writeHeapRowCanonical(ctx, pgConstraintTableRel(ctx), PGConstraintColumnsPG18(), buildPGConstraintRowForTableCheck(tbl, nc))
	return err
}

// stampCheckConstraintRows stamps xmax on every contype='c' TABLE-level row
// (conrelid=relOID) in the given database's pg_constraint heap. Domain CHECK
// rows are untouched — they carry conrelid=0, never relOID. Mirrors
// stampForeignKeyConstraintRows exactly (M0143-0003b); see its comment for
// why the row must be DECODED via the descriptor rather than matched at a
// fixed byte offset, and why the predicate needs both contype and conrelid.
func stampCheckConstraintRows(ctx *Context, dbOid, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgConstraintRelOID, Fork: storage.MainFork}
	cols := PGConstraintColumnsPG18()
	stampCatalogRowsTuple(ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "c" && uint32(decoded[8].Int) == relOID
	})
}

// resolveFKCatalogKeys translates one catalog.ForeignKey from goopg's
// name-keyed in-memory form into PG's pg_constraint coordinates: the
// referenced relation's OID and the 1-based attnum arrays.
//
// Returns ok=false when the referenced table cannot be resolved — the FK is
// then not journalled, exactly as the synthesised view omits it
// (catalog.go:7253-7262 leaves confrelid 0). Writing a row with confrelid=0
// would reload as a dangling FK, which is worse than not persisting it.
//
// confkey is left EMPTY when fk.RefColumns is empty, preserving the documented
// "empty = use the parent's PK" convention (catalog.go:1665) verbatim rather
// than materialising the PK's attnums. Materialising would change what
// pg_get_constraintdef renders, which is out of R126's scope. (PG always
// stores a concrete confkey; that divergence is pre-existing.)
func resolveFKCatalogKeys(ctx *Context, tbl *catalog.Table, fk catalog.ForeignKey) (confrelid uint32, conkey, confkey, confdelsetcols []int16, ok bool) {
	im, isIM := ctx.Catalog.(*catalog.InMemory)
	if !isIM {
		return 0, nil, nil, nil, false
	}
	// catalog.ForeignKey.RefTable is UNSCHEMED (catalog.go:1664), so the
	// referenced table must be found the way the synthesised pg_constraint
	// view finds it — a case-insensitive scan across the database's tables
	// (catalog.go:7253-7262) — not a `public`-qualified lookup. Hardcoding
	// "public" here silently failed to persist any FK whose parent lives in
	// another schema, while the view still displayed it: the exact bug this
	// round exists to fix, reproduced one schema over.
	dbOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	refTbl := im.LookupTableByNameAnySchema(fk.RefTable, dbOid)
	if refTbl == nil {
		return 0, nil, nil, nil, false
	}

	attnum := func(t *catalog.Table, name string) (int16, bool) {
		for i, col := range t.Columns {
			if strings.EqualFold(col.Name, name) {
				return int16(i + 1), true
			}
		}
		return 0, false
	}
	for _, cn := range fk.Columns {
		n, found := attnum(tbl, cn)
		if !found {
			return 0, nil, nil, nil, false
		}
		conkey = append(conkey, n)
	}
	for _, cn := range fk.RefColumns {
		n, found := attnum(refTbl, cn)
		if !found {
			return 0, nil, nil, nil, false
		}
		confkey = append(confkey, n)
	}
	for _, cn := range fk.OnDeleteSetCols {
		n, found := attnum(tbl, cn)
		if !found {
			// Symmetric with the conkey/confkey declines above, and with the
			// reload's own refusal: silently dropping one column here would
			// widen `ON DELETE SET NULL (a)` into an unrestricted SET NULL
			// over the whole key — a behaviour change, not a lost estimate.
			return 0, nil, nil, nil, false
		}
		confdelsetcols = append(confdelsetcols, n)
	}
	return refTbl.OID, conkey, confkey, confdelsetcols, true
}

// writeForeignKeyConstraintRow journals one FK as a pg_constraint heap INSERT
// into the TABLE's database (see pgConstraintTableRel).
//
// Known divergence, recorded rather than fixed: conpfeqop/conppeqop/conffeqop
// stay NULL where PG populates them for every FK. goopg's planner and its
// runtime enforcement read neither, but a PG standby would see the gap.
func writeForeignKeyConstraintRow(ctx *Context, fk catalog.ForeignKey, conrelid, confrelid uint32, conkey, confkey, confdelsetcols []int16) error {
	row, err := buildPGConstraintRowForForeignKey(fk, conrelid, confrelid, conkey, confkey, confdelsetcols)
	if err != nil {
		return err
	}
	_, err = writeHeapRowCanonical(ctx, pgConstraintTableRel(ctx), PGConstraintColumnsPG18(), row)
	return err
}

// writeDomainCheckConstraintRow journals one domain CHECK as a pg_constraint
// heap INSERT (XLOG_HEAP_INSERT).
func writeDomainCheckConstraintRow(ctx *Context, d *catalog.Domain, chk catalog.DomainCheck) error {
	_, err := writeHeapRowCanonical(ctx, pgConstraintRel(ctx), PGConstraintColumnsPG18(),
		buildPGConstraintRowForDomainCheck(d, chk))
	return err
}

// deleteConstraintRowByOID stamps xmax on the pg_constraint row whose oid
// column matches (DROP CONSTRAINT / DROP DOMAIN / pre-update delete).
func deleteConstraintRowByOID(ctx *Context, conOID uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgConstraintRel(ctx), xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == conOID
	})
}

// stampForeignKeyConstraintRows stamps xmax on every contype='f' row whose
// conrelid matches, in the given database's pg_constraint heap.
//
// R126. Two details are load-bearing:
//
//  1. The row is DECODED via the descriptor rather than matched at a fixed
//     byte offset. The pg_attrdef (data[4:8]) and pg_inherits (data[0:4]) arms
//     next door can use raw offsets because those columns sit at the front of
//     the tuple; conrelid is pg_constraint's NINTH column, behind a 64-byte
//     `name`, so a hand-computed offset would be right once and wrong after
//     any layout change or `name` normalisation tweak.
//
//  2. The predicate requires contype='f' as well as conrelid. Domain CHECK
//     rows are safe today only because they carry conrelid=0 — but B3 adds
//     table CHECK rows (contype='c') with a REAL conrelid, and this funnel
//     re-emits FK rows ONLY. A conrelid-only predicate would therefore
//     silently delete every table CHECK row on the next ALTER the moment B3
//     lands. Guarding on contype now makes that a non-event.
func stampForeignKeyConstraintRows(ctx *Context, dbOid, relOID uint32, xmax storage.TransactionID) {
	rel := storage.RelFileNode{DBOid: dbOid, RelOid: pgConstraintRelOID, Fork: storage.MainFork}
	cols := PGConstraintColumnsPG18()
	stampCatalogRowsTuple(ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "f" && uint32(decoded[8].Int) == relOID
	})
}

// resyncForeignKeyCatalogRow stamps and re-emits ONE foreign key's
// pg_constraint row, touching nothing else.
//
// R126: the narrow alternative to syncConstraintCatalogRow, for VALIDATE
// CONSTRAINT — which runs under ShareUpdateExclusiveLock alongside concurrent
// DML and so must not trigger a whole-table catalog rewrite. See the call site
// for the full reasoning.
func (o *ddlOp) resyncForeignKeyCatalogRow(tbl *catalog.Table, fk catalog.ForeignKey) error {
	if !catalogHeapSyncAvailable(o.ctx) {
		return nil
	}
	if fk.Name == "" || fk.OID == 0 {
		return nil
	}
	confrelid, conkey, confkey, setCols, ok := resolveFKCatalogKeys(o.ctx, tbl, fk)
	if !ok {
		return nil
	}
	// The stamp must SUCCEED before the re-write, not merely be attempted.
	// The `if err == nil { stamp }` + unconditional write idiom used by
	// syncConstraintCatalogRow is safe there because that path re-syncs by
	// conrelid and is idempotent; here it would leave TWO live rows carrying
	// the same constraint OID (one convalidated=f, one =t) and the reload
	// would rebuild two ForeignKeys entries for one constraint.
	if err := o.ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	xmax := o.ctx.Tx.XID
	rel := pgConstraintTableRel(o.ctx)
	cols := PGConstraintColumnsPG18()
	// Keyed on the constraint's own OID, not conrelid: the sibling FKs of
	// the same table must survive untouched.
	stampCatalogRowsTuple(o.ctx, rel, xmax, func(ht storage.HeapTuple) bool {
		natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
		decoded := make(Row, len(cols))
		if err := DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); err != nil {
			return false
		}
		return decoded[3].StringValue() == "f" && uint32(decoded[0].Int) == fk.OID
	})
	if err := writeForeignKeyConstraintRow(o.ctx, fk, tbl.OID, confrelid, conkey, confkey, setCols); err != nil {
		return err
	}
	// Unlike the funnel, this path never reaches syncTableToCatalogHeap's
	// mirror step, so without this base/5/2606 would keep saying
	// convalidated='f' until some later table DDL happened to re-mirror.
	// goopg's own reload mains at DefaultDBOid and is unaffected, but a PG
	// standby reading base/5 would see the stale value.
	if tableCatalogHeapDBOid(o.ctx) == catalog.DefaultDBOid {
		mirrorConstraintCatalogFiles(o.ctx)
	}
	return nil
}

// mirrorConstraintCatalogFiles propagates the pg_constraint heap to the
// postgres DB's copy (reload reads base/5 — doc 02a §2.2 BLOCKER-3).
func mirrorConstraintCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConstraintRelOID)
}

// pgTypeIOProcsForOID returns a base type's (typinput, typoutput,
// typreceive, typsend) pg_proc OIDs, sourced from pg_type.dat. PG's
// DefineDomain copies the base's I/O procs into the domain's own pg_type
// row and getTypeOutputInfo reads the DOMAIN row directly, so a zero
// typoutput makes every domain-value render on a real PG standby fail with
// "no output function available". Covers the domain-supported base types;
// unknown OIDs fall back to text's procs (matching TypeNameToOID's own
// text fallback). B2.1b.
func pgTypeIOProcsForOID(oid uint32) (in, out, recv, send int64) {
	switch oid {
	case 16: // bool
		return 1242, 1243, 2436, 2437
	case 17: // bytea
		return 1244, 31, 2412, 2413
	case 20: // int8
		return 460, 461, 2408, 2409
	case 21: // int2
		return 38, 39, 2404, 2405
	case 23: // int4
		return 42, 43, 2406, 2407
	case 700: // float4
		return 200, 201, 2424, 2425
	case 701: // float8
		return 214, 215, 2426, 2427
	case 869: // inet
		return 910, 911, 2420, 2421
	case 1042: // bpchar
		return 1044, 1045, 2430, 2431
	case 1043: // varchar
		return 1046, 1047, 2432, 2433
	case 1082: // date
		return 1084, 1085, 2468, 2469
	case 1083: // time
		return 1143, 1144, 2470, 2471
	case 1114: // timestamp
		return 1312, 1313, 2474, 2475
	case 1184: // timestamptz
		return 1150, 1151, 2476, 2477
	case 1560: // bit
		return 1564, 1565, 2456, 2457
	case 1562: // varbit
		return 1579, 1580, 2458, 2459
	case 1700: // numeric
		return 1701, 1702, 2460, 2461
	case 2950: // uuid
		return 2952, 2953, 2961, 2962
	default: // text and anything unregistered
		return 46, 47, 2414, 2415
	}
}
