package catalog

import "testing"

func TestSharedCatalogRelFileNode(t *testing.T) {
	rel := SharedCatalogRelFileNode(PgDatabaseRelationOID)
	if rel.DBOid != 0 {
		t.Errorf("DBOid = %d, want 0", rel.DBOid)
	}
	if rel.RelOid != 1262 {
		t.Errorf("RelOid = %d, want 1262", rel.RelOid)
	}
}

func TestPgDatabaseColumnsPG18Shape(t *testing.T) {
	cols := PgDatabaseColumnsPG18()
	if len(cols) != 18 {
		t.Fatalf("len(cols) = %d, want 18", len(cols))
	}
	if cols[PgDatabaseDatFrozenXIDOrdinal].Name != "datfrozenxid" {
		t.Fatalf("cols[%d].Name = %q, want datfrozenxid", PgDatabaseDatFrozenXIDOrdinal, cols[PgDatabaseDatFrozenXIDOrdinal].Name)
	}
	for i, c := range cols {
		if c.Ordinal != i {
			t.Errorf("cols[%d].Ordinal = %d, want %d", i, c.Ordinal, i)
		}
	}
}
