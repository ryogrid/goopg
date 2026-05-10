package executor

import (
	"bytes"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

func textCols() []catalog.Column {
	return []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "bid", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "abalance", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
		{Name: "filler", Type: catalog.Type{Name: "text"}, Ordinal: 3},
	}
}

// TestEncodeCopyTextRowPgbenchShape: pgbench loads pgbench_accounts
// with three integers and an empty filler — the exact shape we
// commit to supporting.
func TestEncodeCopyTextRowPgbenchShape(t *testing.T) {
	cols := textCols()
	row := Row{
		{Kind: KindInt, Int: 1},
		{Kind: KindInt, Int: 1},
		{Kind: KindInt, Int: 0},
		NewStringDatum(""),
	}
	got, err := EncodeCopyTextRow(nil, row, cols)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("1\t1\t0\t\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestEncodeCopyTextRowEscaping pins the four escape characters
// goopg emits — backslash, newline, carriage return, tab — and
// verifies the rest of the byte stream passes through untouched.
func TestEncodeCopyTextRowEscaping(t *testing.T) {
	cols := []catalog.Column{{Name: "s", Type: catalog.Type{Name: "text"}}}
	row := Row{NewStringDatum("a\\b\nc\rd\te")}
	got, err := EncodeCopyTextRow(nil, row, cols)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("a\\\\b\\nc\\rd\\te\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestEncodeCopyTextRowNullSentinel: NULL renders as `\N` per
// upstream's text-format default.
func TestEncodeCopyTextRowNullSentinel(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
	row := Row{NullDatum, NewStringDatum("x")}
	got, err := EncodeCopyTextRow(nil, row, cols)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\\N\tx\n" {
		t.Errorf("got %q, want %q", got, "\\N\tx\n")
	}
}

// TestDecodeCopyTextRowPgbenchShape is the round-trip companion to
// the encode test — exactly what pgbench's COPY data looks like.
func TestDecodeCopyTextRowPgbenchShape(t *testing.T) {
	cols := textCols()
	got, err := DecodeCopyTextRow([]byte("42\t1\t100\thello"), cols)
	if err != nil {
		t.Fatal(err)
	}
	want := Row{
		{Kind: KindInt, Int: 42},
		{Kind: KindInt, Int: 1},
		{Kind: KindInt, Int: 100},
		NewStringDatum("hello"),
	}
	if !rowsEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// TestDecodeCopyTextRowNull: the bare \N field decodes to NULL but
// the same two bytes appearing inside a longer field decode to
// literal "N".
func TestDecodeCopyTextRowNull(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "text"}},
	}
	got, err := DecodeCopyTextRow([]byte("\\N\tx\\Ny"), cols)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].IsNull() {
		t.Errorf("col 0 not null: %+v", got[0])
	}
	if got[1].Kind != KindString || got[1].StringValue() != "xNy" {
		t.Errorf("col 1 = %+v want xNy", got[1])
	}
}

// TestDecodeCopyTextRowEscapes covers each escape sequence the
// codec recognises so the table stays in one place.
func TestDecodeCopyTextRowEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`\b`, "\x08"},
		{`\f`, "\x0c"},
		{`\n`, "\n"},
		{`\r`, "\r"},
		{`\t`, "\t"},
		{`\v`, "\x0b"},
		{`\\`, "\\"},
		{`\x41`, "A"},
		{`\X41`, "A"},
		{`\101`, "A"},
		{`\1`, "\x01"},
		{`\q`, "q"},
	}
	cols := []catalog.Column{{Name: "s", Type: catalog.Type{Name: "text"}}}
	for _, tc := range cases {
		got, err := DecodeCopyTextRow([]byte(tc.in), cols)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got[0].Kind != KindString || got[0].StringValue() != tc.want {
			t.Errorf("%q -> %+v, want %q", tc.in, got[0], tc.want)
		}
	}
}

// TestDecodeCopyTextRowFieldCount surfaces a clean error when the
// row length doesn't match the schema — the executor needs that to
// turn into a SQLSTATE 22P04 (bad_copy_file_format) at the wire
// boundary.
func TestDecodeCopyTextRowFieldCount(t *testing.T) {
	cols := textCols()
	if _, err := DecodeCopyTextRow([]byte("1\t2\t3"), cols); err == nil {
		t.Errorf("expected error for short row")
	}
	if _, err := DecodeCopyTextRow([]byte("1\t2\t3\t4\t5"), cols); err == nil {
		t.Errorf("expected error for long row")
	}
}

// TestRoundTripBoolAndTimestamp checks that bool/timestamp encode
// to the lexical forms the decoder accepts and survive a round trip.
func TestRoundTripBoolAndTimestamp(t *testing.T) {
	cols := []catalog.Column{
		{Name: "active", Type: catalog.Type{Name: "bool"}},
		{Name: "ts", Type: catalog.Type{Name: "timestamp"}},
	}
	now := time.Date(2026, 4, 28, 12, 34, 56, 789000*1000, time.UTC)
	row := Row{
		NewBoolDatum(true),
		NewTimeDatum(now),
	}
	enc, err := EncodeCopyTextRow(nil, row, cols)
	if err != nil {
		t.Fatal(err)
	}
	// Strip trailing newline before decoding.
	if enc[len(enc)-1] != '\n' {
		t.Fatal("missing trailing newline")
	}
	got, err := DecodeCopyTextRow(enc[:len(enc)-1], cols)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Kind != KindBool || got[0].BoolValue() != true {
		t.Errorf("bool: %+v", got[0])
	}
	if got[1].Kind != KindTime || !got[1].TimeValue().Equal(now) {
		t.Errorf("ts: %v want %v", got[1].TimeValue(), now)
	}
}

func rowsEqual(a, b Row) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Kind != b[i].Kind {
			return false
		}
		switch a[i].Kind {
		case KindInt:
			if a[i].Int != b[i].Int {
				return false
			}
		case KindBool:
			if a[i].BoolValue() != b[i].BoolValue() {
				return false
			}
		case KindString:
			if a[i].StringValue() != b[i].StringValue() {
				return false
			}
		case KindBytes:
			if !bytes.Equal(a[i].BytesValue(), b[i].BytesValue()) {
				return false
			}
		case KindTime:
			if !a[i].TimeValue().Equal(b[i].TimeValue()) {
				return false
			}
		}
	}
	return true
}
