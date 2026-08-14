package wal

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// A user array column is catalog.Type{Name:<ELEMENT type>, IsArray:true} — Name
// is the ELEMENT's name, never "int4[]" and never "_int4". pgoPhysicalAlign and
// pgoDecodePhysicalValue used to switch on Name alone, so every array column was
// decoded as its scalar element type: a `uuid[]` column's ArrayType blob was
// read as a bare 16-byte pg_uuid_t from offset 0. That is worse than a wrong
// value — the wrong byte width mis-advances the offset of every FOLLOWING
// column in the tuple, so a single array column corrupts the whole replicated
// row. M0119-0006.
//
// Blobs here are built by hand against the layout documented in
// internal/pgarray (24-byte header + elements at their typalign), which is what
// executor.encodeArrayValuePG writes; the expected texts are PostgreSQL 18.3
// array_out output.

// arrayBlob builds the 1-D no-NULL ArrayType varlena goopg writes: 4-byte
// varlena header, ndim=1, dataoffset=0, elemtype, dims[0]=n, lbound[0]=1, then
// the element bodies (already aligned by the caller).
func arrayBlob(elemOID uint32, n int, body []byte) []byte {
	total := 24 + len(body)
	b := make([]byte, total)
	binary.LittleEndian.PutUint32(b[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(b[4:8], 1)
	binary.LittleEndian.PutUint32(b[8:12], 0)
	binary.LittleEndian.PutUint32(b[12:16], elemOID)
	binary.LittleEndian.PutUint32(b[16:20], uint32(n))
	binary.LittleEndian.PutUint32(b[20:24], 1)
	copy(b[24:], body)
	return b
}

func le32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

func le64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

// byteaElem is one bytea array element: the same 4-byte varlena header as a text
// element, over the RAW bytes rather than text.
func byteaElem(raw []byte) []byte {
	b := make([]byte, 4+len(raw))
	binary.LittleEndian.PutUint32(b[0:4], uint32(4+len(raw))<<2)
	copy(b[4:], raw)
	return b
}

// textElem is one varlena array element: 4-byte header (len<<2) + payload.
func textElem(s string) []byte {
	b := make([]byte, 4+len(s))
	binary.LittleEndian.PutUint32(b[0:4], uint32(4+len(s))<<2)
	copy(b[4:], s)
	return b
}

func TestPgoDecodeArrayColumns(t *testing.T) {
	// uuid elements: pg_type 2950, typlen 16, typalign 'c' (no padding).
	uuidA := []byte{0xa0, 0xee, 0xbc, 0x99, 0x9c, 0x0b, 0x4e, 0xf8, 0xbb, 0x6d, 0x6b, 0xb9, 0xbd, 0x38, 0x0a, 0x11}
	uuidB := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}

	// interval elements: pg_type 1186, typlen 16, typalign 'd' (already 8-aligned
	// at body offsets 0 and 16). Element bodies are {micros, days, months}.
	ivl := func(months, days int32, micros int64) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint64(b[:8], uint64(micros))
		binary.LittleEndian.PutUint32(b[8:12], uint32(days))
		binary.LittleEndian.PutUint32(b[12:16], uint32(months))
		return b
	}

	cases := []struct {
		name   string
		typ    catalog.Type
		blob   []byte
		want   string
		regOut func(string, uint32) string
	}{
		{
			name: "int4[]",
			typ:  catalog.Type{Name: "int4", IsArray: true},
			blob: arrayBlob(23, 2, append(le32(1), le32(2)...)),
			want: "{1,2}",
		},
		{
			name: "text[] quotes what array_out quotes",
			typ:  catalog.Type{Name: "text", IsArray: true},
			// "a b" is a 7-byte varlena, so the second element is padded to the
			// next 4-byte boundary (typalign 'i'), exactly as the encoder writes it.
			blob: arrayBlob(25, 2, append(append(textElem("a b"), 0), textElem("c")...)),
			want: `{"a b",c}`,
		},
		{
			// The regression that motivated the fix: read as a scalar uuid this
			// returned the varlena header bytes of the blob as a "uuid".
			name: "uuid[]",
			typ:  catalog.Type{Name: "uuid", IsArray: true},
			blob: arrayBlob(2951, 2, append(append([]byte{}, uuidA...), uuidB...)),
			want: "{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11,00112233-4455-6677-8899-aabbccddeeff}",
		},
		{
			name: "interval[]",
			typ:  catalog.Type{Name: "interval", IsArray: true},
			blob: arrayBlob(1187, 2, append(ivl(1, 0, 0), ivl(0, 0, 2*3600*1_000_000)...)),
			want: `{"1 mon",02:00:00}`,
		},
		// The date-time and bytea element images (M0119-0006 part 2). Before the
		// element table covered them a subscriber received these columns as TEXT
		// bodies under an elemtype-25 header; now the SAME bytes the heap holds
		// must render as upstream's type output. Expected texts from PG 18.3 with
		// TimeZone=UTC.
		{
			name: "date[]",
			typ:  catalog.Type{Name: "date", IsArray: true},
			// 7305 = days from 2000-01-01 to 2020-01-01; 7836 = 2021-06-15.
			blob: arrayBlob(1082, 2, append(le32(uint32(int32(7305))), le32(uint32(int32(7836)))...)),
			want: "{2020-01-01,2021-06-15}",
		},
		{
			// The timestamp text contains a space, so array_out quotes it — a
			// renderer that skipped the quoting would produce an array literal a
			// subscriber re-parses into two elements.
			name: "timestamp[]",
			typ:  catalog.Type{Name: "timestamp", IsArray: true},
			blob: arrayBlob(1114, 1, le64(uint64(7305*86400*int64(1_000_000)+10*3600*int64(1_000_000)))),
			want: `{"2020-01-01 10:00:00"}`,
		},
		{
			name: "bytea[]",
			typ:  catalog.Type{Name: "bytea", IsArray: true},
			// Raw bytes behind the 4-byte element header, at typalign 'i': the
			// first element is 4+1 bytes and the second starts at the next
			// 4-boundary.
			blob: arrayBlob(17, 2, append(append(byteaElem([]byte{0x01}), 0, 0, 0),
				byteaElem([]byte{0x01, 0x02, 0xff})...)),
			want: `{"\\x01","\\x0102ff"}`,
		},
		{
			name: "empty array",
			typ:  catalog.Type{Name: "int4", IsArray: true},
			blob: func() []byte {
				b := make([]byte, 16)
				binary.LittleEndian.PutUint32(b[0:4], uint32(16)<<2)
				binary.LittleEndian.PutUint32(b[12:16], 23)
				return b
			}(),
			want: "{}",
		},
		// reg*[] element name rendering (M0119-0006, deferral row 1353). The
		// stored element is the same 4-byte LE OID as the scalar family; TEXT-mode
		// pgoutput emits a reg* element as its typoutput NAME (proto.c:848), so a
		// threaded renderer yields {name,...} and a nil renderer (no catalog)
		// yields the numeric-OID {oid,...}. The synthetic closure stands in for
		// executor.RegOutRenderer — internal/wal cannot import the executor.
		{
			name: "regclass[] with renderer renders names",
			typ:  catalog.Type{Name: "regclass", IsArray: true},
			blob: arrayBlob(2205, 2, append(le32(1259), le32(1259)...)),
			want: "{pg_class,pg_class}",
			regOut: func(_ string, oid uint32) string {
				if oid == 1259 {
					return "pg_class"
				}
				return strconv.FormatUint(uint64(oid), 10)
			},
		},
		{
			name: "regclass[] nil renderer stays numeric",
			typ:  catalog.Type{Name: "regclass", IsArray: true},
			blob: arrayBlob(2205, 2, append(le32(1259), le32(1259)...)),
			want: "{1259,1259}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n, err := pgoDecodePhysicalValue(tc.typ, tc.blob, tc.regOut)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue: %v", err)
			}
			if n != len(tc.blob) {
				t.Errorf("consumed %d bytes, want %d (the whole varlena)", n, len(tc.blob))
			}
			if string(got) != tc.want {
				t.Errorf("decoded %q, want %q", got, tc.want)
			}
		})
	}
}

// Every ArrayType is a varlena, i.e. PG 'i' / 4-byte alignment — never the
// ELEMENT's alignment. `interval[]` is the sharp case: the element's typalign
// is 'd', so the pre-fix code aligned an array column to 8.
func TestPgoPhysicalAlignArrayIsVarlenaAlign(t *testing.T) {
	for _, elem := range []string{"interval", "int8", "uuid", "bool", "int2"} {
		typ := catalog.Type{Name: elem, IsArray: true}
		if got := pgoPhysicalAlign(4, typ); got != 4 {
			t.Errorf("pgoPhysicalAlign(4, %s[]) = %d, want 4", elem, got)
		}
		if got := pgoPhysicalAlign(5, typ); got != 8 {
			t.Errorf("pgoPhysicalAlign(5, %s[]) = %d, want 8", elem, got)
		}
	}
}

// The offset carry: an array column must not shift the columns after it. This
// is the half a per-value test cannot catch — pre-fix, the `uuid[]` column
// consumed 16 bytes instead of the blob's full length and the int4 that follows
// decoded from the middle of the array body.
func TestEncodePgoTuplePhysicalArrayDoesNotShiftFollowingColumn(t *testing.T) {
	blob := arrayBlob(23, 2, append(le32(7), le32(8)...)) // int4[] {7,8}, 32 bytes
	body := append(append([]byte{}, blob...), le32(42)...)
	cols := []ColumnDef{
		{Name: "a", Type: catalog.Type{Name: "int4", IsArray: true}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}
	out, err := encodePgoTuplePhysical(cols, body, []byte{0x03}, 2, nil)
	if err != nil {
		t.Fatalf("encodePgoTuplePhysical: %v", err)
	}
	vals := parsePgoTupleTextValues(t, out)
	if len(vals) != 2 {
		t.Fatalf("got %d columns, want 2", len(vals))
	}
	if vals[0] != "{7,8}" {
		t.Errorf("array column decoded %q, want \"{7,8}\"", vals[0])
	}
	if vals[1] != "42" {
		t.Errorf("column after the array decoded %q, want \"42\" (offset carry)", vals[1])
	}
}

// parsePgoTupleTextValues reads the tuple body encodePgoTuplePhysical emits:
// uint16 natts, then per column a status byte ('n' or 't') and, for 't', a
// uint32 length followed by the text.
func parsePgoTupleTextValues(t *testing.T, buf []byte) []string {
	t.Helper()
	if len(buf) < 2 {
		t.Fatalf("tuple body too short: %d bytes", len(buf))
	}
	n := int(binary.BigEndian.Uint16(buf[:2]))
	off := 2
	vals := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if off >= len(buf) {
			t.Fatalf("truncated tuple at column %d", i)
		}
		switch buf[off] {
		case pgoColNull:
			off++
			vals = append(vals, "\x00NULL")
		case pgoColText:
			off++
			sz := int(binary.BigEndian.Uint32(buf[off : off+4]))
			off += 4
			vals = append(vals, string(buf[off:off+sz]))
			off += sz
		default:
			t.Fatalf("unexpected column status byte %q", buf[off])
		}
	}
	return vals
}

// End-to-end over the plugin: the `R` message must advertise the ARRAY pg_type
// OID (_int4 = 1007), not the element's (23). Name alone yields the element OID
// because a goopg array column is Type{Name:<ELEMENT>, IsArray:true}, so the
// subscriber was told an `int4[]` column is a plain int4 while the values on the
// wire are array text — an apply worker parsing "{7,8}" as int4 errors out.
// M0119-0006.
func TestPgOutputRelationAdvertisesArrayTypeOID(t *testing.T) {
	cols := []catalog.Column{
		{Name: "tags", Type: catalog.Type{Name: "int4", IsArray: true}, Ordinal: 0},
		{Name: "id", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	}
	snap, rel := snapshotForRel(t, "arr_items", cols)

	var buf bytes.Buffer
	po := NewPgOutput(snap, &buf)
	body := append(arrayBlob(23, 2, append(le32(7), le32(8)...)), le32(42)...)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: wrapAsHeapTuple(t, body, 2)}); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if out[0] != 'R' {
		t.Fatalf("first message kind=%q want R", out[0])
	}
	// R framing: kind(1) | rel_oid(4) | schema\0 | name\0 | replident(1) |
	// natts(2) | per column: flags(1) | name\0 | type_oid(4) | typmod(4).
	off := 5
	for n := 0; n < 2; n++ { // skip schema and relation name
		off += bytes.IndexByte(out[off:], 0) + 1
	}
	off += 1 + 2 // replica identity + natts
	off += 1     // column flags
	off += bytes.IndexByte(out[off:], 0) + 1
	if got := binary.BigEndian.Uint32(out[off : off+4]); got != 1007 {
		t.Errorf("R advertises type OID %d for int4[], want 1007 (_int4)", got)
	}

	idx := bytes.IndexByte(out, 'I')
	if idx < 0 {
		t.Fatal("no I message in output")
	}
	vals := parsePgoTupleTextValues(t, out[idx+6:])
	if len(vals) != 2 || vals[0] != "{7,8}" || vals[1] != "42" {
		t.Errorf("insert values = %q, want [{7,8} 42]", vals)
	}
}
