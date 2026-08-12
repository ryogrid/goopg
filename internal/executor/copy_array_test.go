package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// COPY of an ARRAY column. Both halves of the COPY codec dispatch on the
// element type name — a user array column is catalog.Type{Name:<ELEMENT>,
// IsArray:true} — so before M0119-0006's 43rd slice every non-text array column
// made `COPY … TO` hard-error ("expected int datum for int4, got kind 3" for an
// int4[] column, "expected time datum for date, got kind 3" for a date[] one)
// and `COPY … FROM` reject its own output ("invalid integer \"{1,2}\"").
// text[] alone worked, by falling through to the default arm.
//
// Upstream renders an array column through the COLUMN's output function, i.e.
// array_out (CopyOneRowTo, postgres/src/backend/commands/copyto.c); goopg has
// already produced that text at heap-decode time, so the codec's job is to pass
// it through and let the TEXT escaper / CSV quoter treat it as any other
// string.
//
// Expected strings are PostgreSQL 18.3's, captured live on the reference
// cluster (port 65432) with the same literals:
//
//	CREATE TABLE zz_arr(i int4[], d date[], t text[], ts timestamp[],
//	                    n numeric[], q text[]);
//	INSERT INTO zz_arr VALUES ('{1,2}','{2020-06-15}','{a,b}',
//	    '{"2020-06-15 10:00:00"}','{1.50}','{"has,comma"}');
//	COPY zz_arr TO STDOUT;
//	COPY zz_arr TO STDOUT WITH (FORMAT csv);
//
// M0119-0006.

// arrayCopyCols is the six-column array shape both row-encoder tests use, in
// the order of the captured oracle line.
func arrayCopyCols() []catalog.Column {
	return []catalog.Column{
		{Name: "i", Type: catalog.Type{Name: "int4", IsArray: true}},
		{Name: "d", Type: catalog.Type{Name: "date", IsArray: true}},
		{Name: "t", Type: catalog.Type{Name: "text", IsArray: true}},
		{Name: "ts", Type: catalog.Type{Name: "timestamp", IsArray: true}},
		{Name: "n", Type: catalog.Type{Name: "numeric", IsArray: true}},
		{Name: "q", Type: catalog.Type{Name: "text", IsArray: true}},
	}
}

// arrayCopyRow is the heap-decode output for those columns: an array datum is
// its rendered "{…}" text (decodeArrayValuePGStyled → NewStringDatum).
func arrayCopyRow() Row {
	return Row{
		NewStringDatum(`{1,2}`),
		NewStringDatum(`{2020-06-15}`),
		NewStringDatum(`{a,b}`),
		NewStringDatum(`{"2020-06-15 10:00:00"}`),
		NewStringDatum(`{1.50}`),
		NewStringDatum(`{"has,comma"}`),
	}
}

// TestCopyTextRowEmitsArrayColumns is the primary gate: the TEXT line for a row
// of array columns is byte-identical to PG 18.3's. Before the fix this returned
// an error on the very first (int4[]) column.
func TestCopyTextRowEmitsArrayColumns(t *testing.T) {
	got, err := EncodeCopyTextRow(nil, arrayCopyRow(), arrayCopyCols(), "ISO", "MDY", "")
	if err != nil {
		t.Fatalf("EncodeCopyTextRow: %v", err)
	}
	const want = "{1,2}\t{2020-06-15}\t{a,b}\t{\"2020-06-15 10:00:00\"}\t{1.50}\t{\"has,comma\"}\n"
	if string(got) != want {
		t.Errorf("COPY TEXT line =\n%q\nwant (PG 18.3)\n%q", string(got), want)
	}
}

// TestCopyCsvRowQuotesArrayColumns is the CSV sibling. It matters beyond
// "arrays work at all": the array text is quoted by the ORDINARY field quoter,
// so an element containing the delimiter gets the whole array quoted and its
// inner quotes doubled — which is exactly what upstream does, and would not
// happen if the array arm did its own quoting.
func TestCopyCsvRowQuotesArrayColumns(t *testing.T) {
	f := copyToFormatFromOptions([]parser.CopyOption{{Name: "format", Value: "csv"}})
	got, err := EncodeCopyCsvRow(nil, arrayCopyRow(), arrayCopyCols(), f, "ISO", "MDY", "")
	if err != nil {
		t.Fatalf("EncodeCopyCsvRow: %v", err)
	}
	const want = `"{1,2}",{2020-06-15},"{a,b}","{""2020-06-15 10:00:00""}",{1.50},"{""has,comma""}"` + "\n"
	if string(got) != want {
		t.Errorf("COPY CSV line =\n%q\nwant (PG 18.3)\n%q", string(got), want)
	}
}

// TestCopyTextArrayRoundTripsThroughItsOwnOutput is the Rule #2 sibling check:
// DecodeCopyTextRow must accept what EncodeCopyTextRow wrote, for every array
// element type — the reason copyTextToDatum needed the same IsArray guard.
// The decoded datum is the array TEXT (what encodeValuePG's IsArray branch
// feeds encodeArrayValuePG), so it must come back spelled as it went out.
func TestCopyTextArrayRoundTripsThroughItsOwnOutput(t *testing.T) {
	cols, row := arrayCopyCols(), arrayCopyRow()
	line, err := EncodeCopyTextRow(nil, row, cols, "ISO", "MDY", "")
	if err != nil {
		t.Fatalf("EncodeCopyTextRow: %v", err)
	}
	back, err := DecodeCopyTextRow([]byte(strings.TrimSuffix(string(line), "\n")), cols, `\N`, "")
	if err != nil {
		t.Fatalf("DecodeCopyTextRow: %v", err)
	}
	if len(back) != len(row) {
		t.Fatalf("round trip gave %d datums, want %d", len(back), len(row))
	}
	for i := range row {
		if back[i].Kind != KindString {
			t.Errorf("col %s: kind %d, want KindString (the array-text carrier)", cols[i].Name, back[i].Kind)
		}
		if got, want := back[i].StringValue(), row[i].StringValue(); got != want {
			t.Errorf("col %s: round trip = %q, want %q", cols[i].Name, got, want)
		}
	}
}

// TestCopyTextArrayTextReachesTheEncoder closes the loop on the round trip:
// the text COPY FROM produces is not merely equal to what went in, it is a
// value the heap encoder accepts. A guard that returned, say, a KindBytes datum
// would satisfy the test above's spelling check and still fail to store.
func TestCopyTextArrayTextReachesTheEncoder(t *testing.T) {
	cols, row := arrayCopyCols(), arrayCopyRow()
	for i, c := range cols {
		// Feed each column back its own oracle text — the literal COPY FROM
		// would be handed after DecodeCopyTextRow unescaped the field.
		d, err := copyTextToDatum(c.Type, []byte(row[i].StringValue()), "")
		if err != nil {
			t.Fatalf("%s[]: copyTextToDatum: %v", c.Type.Name, err)
		}
		if _, err := encodeValuePG(c.Type, d); err != nil {
			t.Errorf("%s[]: encodeValuePG rejected the COPY FROM datum: %v", c.Type.Name, err)
		}
	}
}

// TestCopyBinaryRefusesArrayColumns pins the deliberate refusal (0A000) rather
// than leaving the two pre-existing failure modes in place: int4[] reported a
// confusing kind mismatch, while text[]/bytea[] fell through to the raw-bytes
// arm and SILENTLY shipped "{a,b}" where upstream ships array_send's binary
// shape. Delete this test when array_send/array_recv are ported.
func TestCopyBinaryRefusesArrayColumns(t *testing.T) {
	for _, name := range []string{"int4", "text", "bytea", "date", "numeric"} {
		at := catalog.Type{Name: name, IsArray: true}
		_, err := datumToCopyBinary(at, NewStringDatum(`{1}`))
		assertBinaryArrayRefusal(t, name+"[] out", err)
		_, err = copyBinaryToDatum(at, []byte{0, 0, 0, 1})
		assertBinaryArrayRefusal(t, name+"[] in", err)
	}
	// The scalar of the same name must be untouched — the guard keys on
	// IsArray, not on the type name.
	if _, err := datumToCopyBinary(catalog.Type{Name: "int4"}, Datum{Kind: KindInt, Int: 7}); err != nil {
		t.Errorf("scalar int4 binary encode broke: %v", err)
	}
}

func assertBinaryArrayRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: binary COPY accepted an array column", what)
		return
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Errorf("%s: error %v is not an ExecError (no SQLSTATE reaches the client)", what, err)
		return
	}
	if ee.Code != "0A000" {
		t.Errorf("%s: SQLSTATE %s, want 0A000 feature_not_supported", what, ee.Code)
	}
	if !strings.Contains(ee.Message, "array") {
		t.Errorf("%s: message %q does not name the offending type", what, ee.Message)
	}
}
