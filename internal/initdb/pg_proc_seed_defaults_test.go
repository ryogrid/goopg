package initdb

import "testing"

// wantSlotFuncDefaults is the EXACT proargdefaults text a stock PostgreSQL
// 18.3 initdb produces for pg_create_physical_replication_slot (OID 3779),
// captured with
//
//	initdb -D /tmp/probe && psql -tAc \
//	  "select pronargdefaults, proargdefaults from pg_proc where oid=3779"
//
// Two bool Consts (DEFAULT false, DEFAULT false). Note constlen is 1 but the
// datum dump is the full 8-byte Datum word — outfuncs.c:outDatum always emits
// sizeof(Datum) bytes for a by-value type.
const wantSlotFuncDefaults = `({CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 ` +
	`:constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]} ` +
	`{CONST :consttype 16 :consttypmod -1 :constcollid 0 :constlen 1 ` +
	`:constbyval true :constisnull false :location -1 :constvalue 1 [ 0 0 0 0 0 0 0 0 ]})`

// TestPgProcSeedArgDefaultsMatchesRealPG pins the rendered pg_node_tree to the
// real-PG bytes. A real PG 18.3 attached to goopg's cluster directory runs
// stringToNode() on this column from parse_func.c:func_get_detail, so any drift
// (field order, datum width, list punctuation) is an ERROR on the standby, not
// a cosmetic diff.
func TestPgProcSeedArgDefaultsMatchesRealPG(t *testing.T) {
	n, tree := pgProcSeedArgDefaults(3779)
	if n != 2 {
		t.Errorf("pronargdefaults for 3779: got %d, want 2", n)
	}
	if tree != wantSlotFuncDefaults {
		t.Errorf("proargdefaults for 3779:\n got %s\nwant %s", tree, wantSlotFuncDefaults)
	}
}

// TestPgProcSeedArgDefaultsUnlistedOIDsUnchanged guards the 3395 rows that have
// no system_functions.sql DEFAULT clause: they must keep pronargdefaults=0 and
// an EMPTY proargdefaults. A non-empty value there would send a PG standby into
// stringToNode() for a function that has no defaults at all.
func TestPgProcSeedArgDefaultsUnlistedOIDsUnchanged(t *testing.T) {
	// 3780 = pg_drop_replication_slot (1 arg, no defaults); 89 = version().
	for _, oid := range []uint32{89, 3780, 4220} {
		n, tree := pgProcSeedArgDefaults(oid)
		if n != 0 || tree != "" {
			t.Errorf("oid %d: got (%d, %q), want (0, \"\")", oid, n, tree)
		}
	}
}

// TestPgProcRowCarriesSeedDefaults checks the wiring into the 30-column seed
// row itself — columns 18 (pronargdefaults) and 24 (proargdefaults). The
// serializer being correct is worthless if the row builder still writes 0/"".
func TestPgProcRowCarriesSeedDefaults(t *testing.T) {
	var slotEntry, plainEntry *pgProcEntry
	for i, e := range pgProcAllEntries() {
		switch e.OID {
		case 3779:
			slotEntry = &pgProcAllEntries()[i]
		case 3780:
			plainEntry = &pgProcAllEntries()[i]
		}
	}
	if slotEntry == nil || plainEntry == nil {
		t.Fatal("pg_proc seed data is missing OID 3779 and/or 3780")
	}

	row := pgProcRow(*slotEntry)
	if got := row[17].Int; got != 2 {
		t.Errorf("3779 col 18 pronargdefaults: got %d, want 2", got)
	}
	if got := row[23].StringValue(); got != wantSlotFuncDefaults {
		t.Errorf("3779 col 24 proargdefaults:\n got %s\nwant %s", got, wantSlotFuncDefaults)
	}

	plain := pgProcRow(*plainEntry)
	if got := plain[17].Int; got != 0 {
		t.Errorf("3780 col 18 pronargdefaults: got %d, want 0", got)
	}
	if got := plain[23].StringValue(); got != "" {
		t.Errorf("3780 col 24 proargdefaults: got %q, want empty", got)
	}
}
