package config

import (
	"testing"
)

// TestEncodingTableIntegrity verifies the encoding name table is complete and
// all canonical names resolve. The table mirrors pg_enc2name_tbl (43 entries
// in PG 18.3). M0122-0008.
func TestEncodingTableIntegrity(t *testing.T) {
	// Must have exactly 42 entries (PG 18.3 pg_enc2name_tbl: 0..41).
	if len(pgEncNames) != 42 {
		t.Errorf("pgEncNames has %d entries, want 42 (PG 18.3 pg_enc2name_tbl)", len(pgEncNames))
	}

	// Every canonical name must resolve to itself.
	for i, name := range pgEncNames {
		if got := encodingNameToCanonical(name); got != name {
			t.Errorf("encodingNameToCanonical(%q) = %q, want %q (index %d)", name, got, name, i)
		}
	}

	// Every alias must resolve to a known canonical name.
	for alias, canonical := range pgEncAliases {
		if got := encodingNameToCanonical(alias); got != canonical {
			t.Errorf("encodingNameToCanonical(alias %q) = %q, want %q", alias, got, canonical)
		}
		// The canonical name must exist in pgEncNames.
		found := false
		for _, n := range pgEncNames {
			if n == canonical {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("alias %q maps to %q which is not in pgEncNames", alias, canonical)
		}
	}
}
