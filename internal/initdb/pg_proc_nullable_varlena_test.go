package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/storage"
)

// M0131-S13. The seeded pg_proc rows must leave every ABSENT nullable varlena
// attribute genuinely NULL rather than writing an empty ArrayType shell.
//
// Why this is not cosmetic: PG branches on heap_attisnull for all of them.
// fmgr_info_cxt_security (postgres/src/backend/utils/fmgr/fmgr.c:203-211)
// routes a call through fmgr_security_definer as soon as
// `!heap_attisnull(procedureTuple, Anum_pg_proc_proconfig, NULL)`, and
// TransformGUCArray then trips `Assert(ARR_NDIM(array) == 1)` (guc.c:6411) on
// the zero-dimension shell — measured as a hosted-backend SIGABRT on
// `SELECT 'a'::text || 1` (textanycat, oid 2003, is LANGUAGE SQL). A
// production PG build would instead walk a bogus ArrayType.
//
// The runtime sibling buildPGProcRow (internal/executor/sys_pg_proc.go) has
// always used NullDatum here; pgProcRow was the stale twin, so this guard is
// also the sibling-paths tripwire for the pair.
func TestPgProcSeedLeavesAbsentVarlenaAttrsNull(t *testing.T) {
	// Column indexes are 0-based into the 30-column FormData_pg_proc layout.
	nullable := map[int]string{
		20: "proallargtypes",
		21: "proargmodes",
		22: "proargnames",
		23: "proargdefaults",
		24: "protrftypes",
		26: "probin",
		27: "prosqlbody",
		28: "proconfig",
		29: "proacl",
	}
	entries := pgProcInitialEntries()
	if len(entries) == 0 {
		t.Fatal("pg_proc seed data is empty")
	}
	// Every row's proconfig/proacl/probin/prosqlbody/protrftypes is absent for
	// ALL seed entries (goopg seeds no per-function GUCs, grants, C-language
	// binaries, SQL bodies or transforms), so those four are unconditional.
	// The remaining four are absent only when the entry carries no value.
	for _, e := range entries {
		row := pgProcRow(e)
		if len(row) != 30 {
			t.Fatalf("oid=%d: pgProcRow len=%d, want 30", e.OID, len(row))
		}
		for _, idx := range []int{24, 26, 27, 28, 29} {
			if !row[idx].IsNull() {
				t.Fatalf("oid=%d (%s): %s is NOT NULL — an empty varlena shell here "+
					"aborts a hosted PG (M0131-S13)", e.OID, e.Name, nullable[idx])
			}
		}
		if e.AllArgTypes == nil && !row[20].IsNull() {
			t.Fatalf("oid=%d: proallargtypes is NOT NULL with no OUT-arg metadata", e.OID)
		}
		if e.ArgModes == nil && !row[21].IsNull() {
			t.Fatalf("oid=%d: proargmodes is NOT NULL with no OUT-arg metadata", e.OID)
		}
		if e.ArgNames == nil && !row[22].IsNull() {
			t.Fatalf("oid=%d: proargnames is NOT NULL with no argument names", e.OID)
		}
		if n, _ := pgProcSeedArgDefaults(e.OID); n == 0 && !row[23].IsNull() {
			t.Fatalf("oid=%d: proargdefaults is NOT NULL with pronargdefaults=0", e.OID)
		}
	}

	// Second half: the NULLs must survive the physical encode. Asserting the
	// Row alone would pass even if the tuple writer dropped the bitmap, which
	// is exactly the byte-level state PG reads.
	cols := pgProcColDefs()
	e := entries[0]
	row := pgProcRow(e)
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	bitmap := executor.NullBitmapPG(row)
	if bitmap == nil {
		t.Fatalf("oid=%d: NullBitmapPG returned nil — the tuple would carry no "+
			"HEAP_HASNULL and PG would read the NULL attrs as data", e.OID)
	}
	decoded := make(executor.Row, len(cols))
	if err := executor.DecodeRowIntoMctxPGTuple(decoded, cols, payload, bitmap, len(cols), nil); err != nil {
		t.Fatalf("DecodeRowIntoMctxPGTuple: %v", err)
	}
	for idx, name := range nullable {
		if !row[idx].IsNull() {
			continue // this entry supplies a value; nothing to re-check
		}
		if !decoded[idx].IsNull() {
			t.Fatalf("oid=%d: %s decoded back as NON-NULL from the physical tuple", e.OID, name)
		}
	}
	// The non-null neighbours must still round-trip, so the bitmap is not
	// simply clearing everything.
	if got := decoded[1].StringValue(); got != e.Name {
		t.Fatalf("proname round-trip: got %q, want %q", got, e.Name)
	}
	if got := decoded[25].StringValue(); got != e.HandlerName {
		t.Fatalf("prosrc round-trip: got %q, want %q", got, e.HandlerName)
	}
}

// The seeded tuples written to base/1/1255 must actually carry the null
// bitmap: writeMultiPageHeapRows only stamps HEAP_HASNULL when
// NullBitmapPG returns non-nil, and PG reads the flag, not the Row.
func TestPgProcSeedTupleCarriesHasNullFlag(t *testing.T) {
	cols := pgProcColDefs()
	row := pgProcRow(pgProcEntry{OID: 330, Name: "bthandler", RetType: 325, HandlerName: "bthandler"})
	payload, err := executor.EncodeRowPG(cols, row)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	bitmap := executor.NullBitmapPG(row)
	tuple := storage.NewHeapTupleWithNulls(storage.TransactionID(1), storage.InvalidTransactionID, bitmap, payload)
	if tuple.Header.Infomask&storage.HeapHasNull == 0 {
		t.Fatalf("seeded pg_proc tuple lacks HEAP_HASNULL (infomask=%#x)", tuple.Header.Infomask)
	}
	// Guard against a silent column-count drift between the two sibling
	// definitions of the physical layout.
	if got, want := len(cols), len(executor.PGProcColumnsPG18()); got != want {
		t.Fatalf("pgProcColDefs has %d columns, executor.PGProcColumnsPG18 has %d", got, want)
	}
	var _ catalog.Column = cols[0]
}
