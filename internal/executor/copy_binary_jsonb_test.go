package executor

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0119-0006 (56th slice). `copy_binary.go` had NO arm for `jsonb` in either
// direction, so both halves fell through to the default's KindString case:
//
//	encode: the bare JSON text went out with NO leading version byte. Upstream
//	        jsonb_send (postgres/src/backend/utils/adt/jsonb.c:124) is
//	        pq_sendint8(1) + pq_sendtext(JsonbToCString(...)), so a real client's
//	        jsonb_recv read '{' (0x7b) as the version and raised "unsupported
//	        jsonb version number 123" — every binary COPY of a jsonb column was
//	        unreadable by PostgreSQL.
//	decode: the version byte was NOT stripped, so a stream written by a real PG
//	        landed in the column as "\x01{...}" — text that is no longer valid
//	        JSON, making every later `->`/`->>` raise 22P02 (evalJSONArrow) and
//	        every equality comparison wrong by one leading byte.
//
// These tests fail against that HEAD.

func jsonbCol() catalog.Type { return catalog.Type{Name: "jsonb"} }

// The wire shape upstream sends: version byte 1, then the JSON text verbatim
// (jsonb_send serialises the tree back to a C string — the jsonb BINARY wire
// format is textual after that byte, which is why goopg's text-backed storage
// can be byte-exact here).
func TestCopyBinaryJsonbSendShape(t *testing.T) {
	for _, s := range []string{
		`{"a": 1, "b": [1, 2]}`,
		`[]`,
		`null`,
		`"hello"`,
		`-1.5e300`,
		`true`,
		`{"k": "é unicode ünicode"}`,
	} {
		b, err := datumToCopyBinary(jsonbCol(), NewStringDatum(s))
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", s, err)
		}
		if len(b) != len(s)+1 {
			t.Fatalf("%s: payload = %d bytes, want %d (1 version + text)", s, len(b), len(s)+1)
		}
		if b[0] != 1 {
			t.Fatalf("%s: version byte = %d, want 1", s, b[0])
		}
		if string(b[1:]) != s {
			t.Fatalf("%s: text after version = %q", s, b[1:])
		}
	}
}

// Hard-won Rule #2: the decode twin must reproduce the Datum the HEAP decode
// produces for the same value. A decoder that forgot to strip the version byte
// passes a length check and fails here.
func TestCopyBinaryJsonbRoundTripMatchesHeapDatum(t *testing.T) {
	col := jsonbCol()
	for _, s := range []string{
		`{"a": 1, "b": [1, 2]}`,
		`{}`,
		`[1, 2, 3]`,
		`null`,
		`0`,
		`"a string with , and \" quotes"`,
		`{"nested": {"deep": [null, true, false]}}`,
	} {
		d := NewStringDatum(s)
		wire, err := datumToCopyBinary(col, d)
		if err != nil {
			t.Fatalf("%s: datumToCopyBinary: %v", s, err)
		}
		back, err := copyBinaryToDatum(col, wire)
		if err != nil {
			t.Fatalf("%s: copyBinaryToDatum: %v", s, err)
		}

		heap, err := encodeValuePG(col, d)
		if err != nil {
			t.Fatalf("%s: encodeValuePG: %v", s, err)
		}
		heapBack, _, err := decodePhysicalPGValueMctx(col, heap, nil)
		if err != nil {
			t.Fatalf("%s: decodePhysicalPGValueMctx: %v", s, err)
		}
		if back.Kind != heapBack.Kind || back.Format() != heapBack.Format() {
			t.Fatalf("%s: COPY decode = kind %d %q, heap decode = kind %d %q",
				s, back.Kind, back.Format(), heapBack.Kind, heapBack.Format())
		}
		if back.Format() != s {
			t.Fatalf("round-trip %q -> %q", s, back.Format())
		}
	}
}

// The pin that found the adjacent heap defect in the 53rd (float spelling) and
// 54th (halved xid8) slices, run for this arm too. jsonb's heap image is a
// varlena whose BODY is the same JSON text the COPY payload carries after the
// version byte — so the two must agree byte for byte with exactly a one-byte
// offset, and physicalPGTypeAlign must be pg_type 3802's typalign 'i'.
//
// What this pin CANNOT assert, and what it therefore documents: upstream's heap
// image is a JsonbContainer/JEntry tree, not text (`typstorage 'x'`,
// jsonb_typanalyze/jsonb.h). goopg's storage is text on BOTH sides, which is a
// real divergence a hosted PG would read as garbage — ledgered under M0119-0006,
// and deliberately NOT in scope here because the COPY wire format is textual and
// so is fixable independently.
func TestCopyBinaryJsonbAgreesWithHeapEncode(t *testing.T) {
	col := jsonbCol()
	for _, s := range []string{
		`{"a": 1}`,
		`[1, 2, 3]`,
		`"x"`,
		strings.Repeat(`{"k": [1, 2, 3]}`, 20), // long enough for the 1-byte varlena header to matter
	} {
		wire, err := datumToCopyBinary(col, NewStringDatum(s))
		if err != nil {
			t.Fatalf("datumToCopyBinary: %v", err)
		}
		heap, err := encodeValuePG(col, NewStringDatum(s))
		if err != nil {
			t.Fatalf("encodeValuePG: %v", err)
		}
		// Strip the varlena header the heap image carries (1-byte short form
		// while total <= 127, else the 4-byte form) and compare bodies.
		var body []byte
		if heap[0]&1 == 1 {
			body = heap[1:]
		} else {
			body = heap[4:]
		}
		if string(body) != string(wire[1:]) {
			t.Fatalf("heap body %q != COPY text %q", body, wire[1:])
		}
	}
	if got := physicalPGTypeAlign(col); got != 4 {
		t.Fatalf("physicalPGTypeAlign(jsonb) = %d, want 4 (pg_type 3802 typalign 'i')", got)
	}
}

// jsonb_recv rejects any version but 1 ("unsupported jsonb version number %d")
// and an empty field has no version byte at all. Without the check a future
// version's stream would be stored with its version byte glued to the text.
func TestCopyBinaryJsonbRecvVersionCheck(t *testing.T) {
	col := jsonbCol()
	if _, err := copyBinaryToDatum(col, nil); err == nil {
		t.Fatal("jsonb_recv accepted a zero-length field")
	}
	for _, v := range []byte{0, 2, 0x7b, 0xff} {
		payload := append([]byte{v}, `{"a": 1}`...)
		_, err := copyBinaryToDatum(col, payload)
		if err == nil {
			t.Fatalf("jsonb_recv accepted version byte %d", v)
		}
		if !strings.Contains(err.Error(), "unsupported jsonb version number") {
			t.Fatalf("version %d: err = %v, want the upstream wording", v, err)
		}
	}
}

// jsonb_recv hands the post-version bytes to jsonb_from_cstring, which PARSES
// them; malformed JSON is an error at COPY time, not a poisoned column that
// raises 22P02 on every later `->`.
func TestCopyBinaryJsonbRecvRejectsMalformedJSON(t *testing.T) {
	col := jsonbCol()
	for _, s := range []string{
		``,         // version byte only — the empty string is not a JSON value
		`{`,        // truncated
		`{"a": }`,  // no value
		`{} {}`,    // two values: encoding/json's Decode alone would accept the first
		`nul`,      // not a literal
		`{'a': 1}`, // single quotes
	} {
		payload := append([]byte{1}, s...)
		_, err := copyBinaryToDatum(col, payload)
		if err == nil {
			t.Fatalf("jsonb_recv accepted %q", s)
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "22P02" {
			t.Fatalf("jsonb_recv(%q) err = %v, want 22P02", s, err)
		}
	}
}

// `json` is NOT `jsonb`: json_send IS textsend (postgres/src/backend/utils/adt/
// json.c), i.e. bare text with no version byte. The default arm already emits
// exactly that, and this pin keeps a future edit from "helpfully" giving json
// the version byte too — which would break it in precisely the way jsonb was
// broken before this slice.
func TestCopyBinaryJSONHasNoVersionByte(t *testing.T) {
	const s = `{"a": 1}`
	b, err := datumToCopyBinary(catalog.Type{Name: "json"}, NewStringDatum(s))
	if err != nil {
		t.Fatalf("datumToCopyBinary(json): %v", err)
	}
	if string(b) != s {
		t.Fatalf("json_send = %q, want the bare text %q", b, s)
	}
	back, err := copyBinaryToDatum(catalog.Type{Name: "json"}, b)
	if err != nil {
		t.Fatalf("copyBinaryToDatum(json): %v", err)
	}
	if back.Format() != s {
		t.Fatalf("json round-trip %q -> %q", s, back.Format())
	}
}

// A full binary COPY row through the public writer/parser — the path a real
// client drives — including a NULL, since a NULL jsonb field carries length -1
// and must never reach the version-byte check.
func TestCopyBinaryJsonbRowFraming(t *testing.T) {
	cols := []catalog.Column{
		{Name: "j", Type: jsonbCol()},
		{Name: "n", Type: jsonbCol()},
	}
	const s = `{"a": [1, 2], "b": null}`
	in := Row{NewStringDatum(s), NullDatum}

	buf, err := AppendCopyBinaryRow(nil, in, cols)
	if err != nil {
		t.Fatalf("AppendCopyBinaryRow: %v", err)
	}
	if got := int32(binary.BigEndian.Uint32(buf[2:6])); got != int32(len(s)+1) {
		t.Fatalf("field length header = %d, want %d", got, len(s)+1)
	}
	buf = AppendCopyBinaryTrailer(buf)

	rows, trailer, _, err := ParseCopyBinaryRows(buf, cols)
	if err != nil {
		t.Fatalf("ParseCopyBinaryRows: %v", err)
	}
	if !trailer || len(rows) != 1 {
		t.Fatalf("parsed %d rows, trailer=%v", len(rows), trailer)
	}
	if rows[0][0].Format() != s {
		t.Fatalf("round-trip = %q, want %q", rows[0][0].Format(), s)
	}
	if !rows[0][1].IsNull() {
		t.Fatalf("NULL jsonb came back as %q", rows[0][1].Format())
	}
}

// A jsonb[] column must still be refused loudly rather than shipping the array
// text under a format declared binary (rejectBinaryCopyArray, 50th slice) — the
// new arm dispatches on the ELEMENT type name, so without the guard running
// first an array column would have taken the scalar path.
func TestCopyBinaryJsonbArrayStillRejected(t *testing.T) {
	col := catalog.Type{Name: "jsonb", IsArray: true}
	if _, err := datumToCopyBinary(col, NewStringDatum(`{"{\"a\": 1}"}`)); err == nil {
		t.Fatal("binary COPY accepted a jsonb[] column")
	}
	if _, err := copyBinaryToDatum(col, []byte{1}); err == nil {
		t.Fatal("binary COPY decode accepted a jsonb[] column")
	}
}
