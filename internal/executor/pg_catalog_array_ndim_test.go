package executor

import (
	"encoding/binary"
	"testing"
)

// TestPGTextArrayBytesIsOneDimensional: a text[] catalog value carries
// ndim = 1, as construct_array writes it. With ndim 0 a PG standby read every
// goopg-written proargnames / reloptions / proconfig as '{}' and refused the
// function ("proargnames must have the same number of elements as the
// function has arguments"). An empty list is PG's 16-byte empty array.
// M0149-0015e.
func TestPGTextArrayBytesIsOneDimensional(t *testing.T) {
	b := pgTextArrayBytes([]string{"n", "m"})
	le := binary.LittleEndian
	if got := le.Uint32(b[4:8]); got != 1 {
		t.Fatalf("ndim = %d, want 1", got)
	}
	if got := le.Uint32(b[16:20]); got != 2 {
		t.Fatalf("dim1 = %d, want 2", got)
	}
	elems, _, err := decodePGTextArrayElements(b)
	if err != nil || len(elems) != 2 || elems[0] != "n" || elems[1] != "m" {
		t.Fatalf("round trip: %v %v", elems, err)
	}
	e := pgTextArrayBytes(nil)
	if len(e) != 16 || le.Uint32(e[4:8]) != 0 || le.Uint32(e[12:16]) != 25 {
		t.Fatalf("empty array = %x", e)
	}
	if elems, _, err := decodePGTextArrayElements(e); err != nil || len(elems) != 0 {
		t.Fatalf("empty round trip: %v %v", elems, err)
	}
}

// TestPGProcTypeOIDPseudoTypes: a routine signature naming a pseudo-type
// gets its pg_type OID, not TypeNameToOID's text fallback (RETURNS void was
// written as prorettype 25). M0149-0015e.
func TestPGProcTypeOIDPseudoTypes(t *testing.T) {
	for name, want := range map[string]uint32{"void": 2278, "trigger": 2279, "record": 2249, "anyelement": 2283, "integer": 23, "text": 25} {
		if got := PGProcTypeOID(nil, name, 0); got != want {
			t.Errorf("PGProcTypeOID(%q) = %d, want %d", name, got, want)
		}
	}
}
