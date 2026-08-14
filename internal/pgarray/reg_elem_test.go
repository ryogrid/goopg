package pgarray

import (
	"encoding/binary"
	"strconv"
	"testing"
)

// M0119-0006 reg* array-element slice (deferral 1306). The scalar reg* family
// (66th–68th slices) stores each value as a 4-byte LE OID; these tests pin that
// ElemTypeInfo reports the six reg* members as fixed 4-byte OID elements and
// that the decode path renders an element OID through the threaded RegOut
// renderer (executor.RegOut's contract: OID 0 → "-", dangling → numeric).

// regClassPayload builds a 1-dim regclass[] ArrayType body (payload starts at
// ndim, exactly what RenderTextStyled consumes) holding the given OIDs.
func regClassPayload(elems ...uint32) []byte {
	payload := make([]byte, 20+4*len(elems))
	binary.LittleEndian.PutUint32(payload[0:4], 1)                         // ndim
	binary.LittleEndian.PutUint32(payload[8:12], 2205)                     // elemtype = regclass
	binary.LittleEndian.PutUint32(payload[12:16], uint32(len(elems)))      // dims[0]
	binary.LittleEndian.PutUint32(payload[16:20], 1)                       // lbound[0]
	for i, e := range elems {
		binary.LittleEndian.PutUint32(payload[20+4*i:24+4*i], e)
	}
	return payload
}

// stubRegOut implements executor.RegOut's contract without importing the
// executor: "pg_class" for 1259, "-" for OID 0, else the numeric spelling.
func stubRegOut(_ string, oid uint32) string {
	if oid == 0 {
		return "-"
	}
	if oid == 1259 {
		return "pg_class"
	}
	return strconv.FormatUint(uint64(oid), 10)
}

func TestElemTypeInfoRegStar(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantOID uint32
	}{
		{"regproc", 24},
		{"regprocedure", 2202},
		{"regclass", 2205},
		{"regtype", 2206},
		{"regrole", 4096},
		{"regcollation", 4191},
	} {
		oid, size, align, varlena, ok := ElemTypeInfo(tc.name)
		if !ok {
			t.Errorf("ElemTypeInfo(%q) ok = false, want true", tc.name)
			continue
		}
		if oid != tc.wantOID || size != 4 || align != 4 || varlena {
			t.Errorf("ElemTypeInfo(%q) = (oid %d, size %d, align %d, varlena %v), want (%d, 4, 4, false)",
				tc.name, oid, size, align, varlena, tc.wantOID)
		}
	}
	// cid stays numeric-only (no name form) — the brief explicitly excludes it
	// from the reg* arms (oid has a long-standing numeric arm of its own).
	if _, _, _, _, ok := ElemTypeInfo("cid"); ok {
		t.Error("ElemTypeInfo(cid) ok = true, want false (numeric-only, no name form)")
	}
}

func TestRegArrayElementRenderViaRegOut(t *testing.T) {
	// 1259 → pg_class through the renderer, OID 0 → "-", dangling 9999 → the
	// renderer's numeric spelling — executor.RegOut's contract.
	got, err := RenderTextStyled("regclass", regClassPayload(1259, 0, 9999), OutputStyle{RegOut: stubRegOut})
	if err != nil {
		t.Fatalf("RenderTextStyled: %v", err)
	}
	if want := "{pg_class,-,9999}"; got != want {
		t.Errorf("RenderTextStyled = %q, want %q", got, want)
	}
}

func TestRegArrayElementRenderNilRegOutFallsBackNumeric(t *testing.T) {
	// A nil renderer (the pgoutput decoder and pure-codec callers have no
	// catalog) still honours the contract minus the name lookup: OID 0 → "-",
	// otherwise the numeric spelling.
	got, err := RenderTextStyled("regclass", regClassPayload(1259, 0, 9999), OutputStyle{})
	if err != nil {
		t.Fatalf("RenderTextStyled: %v", err)
	}
	if want := "{1259,-,9999}"; got != want {
		t.Errorf("RenderTextStyled(nil RegOut) = %q, want %q", got, want)
	}
}

func TestRegArrayElementQuoteCollation(t *testing.T) {
	// regcollationout quote_identifiers ("C" renders `"C"` — the 68th slice's
	// pin), and array_out double-quotes the quotes, so the element comes back
	// as `"""C"""`, matching PG 18.3's `ARRAY['C']::regcollation[]` output.
	render := func(typeName string, oid uint32) string {
		if typeName == "regcollation" && oid == 950 {
			return `"C"`
		}
		return stubRegOut(typeName, oid)
	}
	got, err := RenderTextStyled("regcollation", regClassPayload(950), OutputStyle{RegOut: render})
	if err != nil {
		t.Fatalf("RenderTextStyled: %v", err)
	}
	// QuoteTextElem is array_quote's port: " and \ are BACKSLASH-escaped, so the
	// element is `"\"C\""` — matching PG 18.3's `ARRAY['C']::regcollation[]`
	// output (measured), NOT doubled quotes.
	if want := `{"\"C\""}`; got != want {
		t.Errorf("RenderTextStyled(regcollation) = %q, want %q", got, want)
	}
}
