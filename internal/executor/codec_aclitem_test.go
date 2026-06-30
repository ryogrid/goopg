package executor

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// roleOIDFixture is a tiny role registry used by the aclitem codec tests. It
// stands in for the per-role OID registry the heap re-sync path will supply.
var roleOIDFixture = map[string]uint32{
	"postgres": 10,
	"alice":    16385,
	"bob":      16386,
	"grantee":  16390,
}

func fixtureResolveOID(name string) uint32 { return roleOIDFixture[name] }

func fixtureResolveName(oid uint32) string {
	for n, o := range roleOIDFixture {
		if o == oid {
			return n
		}
	}
	return ""
}

func TestAclModeFromPrivLetters(t *testing.T) {
	cases := []struct {
		privs string
		want  uint64
	}{
		{"U", 1 << 8},                            // USAGE
		{"r", 1 << 1},                            // SELECT
		{"X", 1 << 7},                            // EXECUTE
		{"a", 1 << 0},                            // INSERT
		{"m", 1 << 14},                           // MAINTAIN
		{"rwU", (1 << 1) | (1 << 2) | (1 << 8)},  // SELECT/UPDATE/USAGE
		{"U*", (1 << 8) | (uint64(1<<8) << 32)},  // USAGE with grant option
		{"r*w", (1 << 1) | (uint64(1<<1) << 32) | (1 << 2)}, // SELECT*+UPDATE
	}
	for _, tc := range cases {
		got, err := aclModeFromPrivLetters(tc.privs)
		if err != nil {
			t.Fatalf("aclModeFromPrivLetters(%q): %v", tc.privs, err)
		}
		if got != tc.want {
			t.Errorf("aclModeFromPrivLetters(%q) = %#x, want %#x", tc.privs, got, tc.want)
		}
		// Round-trip the mode back to letters.
		if back := aclModeToPrivLetters(got); back != tc.privs {
			t.Errorf("aclModeToPrivLetters(%#x) = %q, want %q", got, back, tc.privs)
		}
	}
}

func TestAclModeFromPrivLettersBadChar(t *testing.T) {
	if _, err := aclModeFromPrivLetters("Z"); err == nil {
		t.Fatalf("expected error for unknown privilege char 'Z'")
	}
}

// TestEncodeAclItemArrayTextGolden pins the exact on-disk bytes for the
// canonical type-grant ACL "{=U/postgres}" (a grant of USAGE to PUBLIC by the
// bootstrap superuser). The layout must match PostgreSQL's _aclitem ArrayType
// (elemtype 1033, one 16-byte AclItem) so a PG18 standby / pg_dump reads it.
func TestEncodeAclItemArrayTextGolden(t *testing.T) {
	blob, err := encodeAclItemArrayText("{=U/postgres}", fixtureResolveOID)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := make([]byte, 0, 40)
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:4], 40<<2)  // varlena header: total 40 bytes
	binary.LittleEndian.PutUint32(hdr[4:8], 1)      // ndim
	binary.LittleEndian.PutUint32(hdr[8:12], 0)     // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(hdr[12:16], 1033) // elemtype = aclitem
	binary.LittleEndian.PutUint32(hdr[16:20], 1)    // dims[0]
	binary.LittleEndian.PutUint32(hdr[20:24], 1)    // lbound[0]
	want = append(want, hdr...)
	elem := make([]byte, 16)
	binary.LittleEndian.PutUint32(elem[0:4], 0)     // ai_grantee = PUBLIC
	binary.LittleEndian.PutUint32(elem[4:8], 10)    // ai_grantor = postgres
	binary.LittleEndian.PutUint64(elem[8:16], 1<<8) // ai_privs = USAGE
	want = append(want, elem...)

	if !bytes.Equal(blob, want) {
		t.Errorf("encodeAclItemArrayText(\"{=U/postgres}\") bytes mismatch\n got=%x\nwant=%x", blob, want)
	}
}

// TestAclItemArrayRoundTrip exercises encode→decode for the canonical aclitemout
// forms goopg's TypeACLText / relacl renderer produces, including PUBLIC (empty
// grantee), multiple grantees, the owner-pulled-to-head form, grant options, and
// multi-privilege relacl-style entries.
func TestAclItemArrayRoundTrip(t *testing.T) {
	cases := []string{
		"{=U/postgres}",                                 // PUBLIC USAGE (type default PUBLIC half)
		"{postgres=U/postgres}",                         // owner-only USAGE
		"{postgres=U/postgres,=U/postgres,alice=U/postgres}", // owner + PUBLIC + grantee (type default after GRANT)
		"{alice=U*/postgres}",                           // grant option
		"{postgres=arwdDxt/postgres,bob=r/postgres}",    // relacl-style multi-priv + single
		"{bob=rwU/alice}",                               // non-owner grantor
	}
	for _, in := range cases {
		blob, err := encodeAclItemArrayText(in, fixtureResolveOID)
		if err != nil {
			t.Fatalf("encode(%q): %v", in, err)
		}
		got, err := decodeAclItemArrayText(blob, fixtureResolveName)
		if err != nil {
			t.Fatalf("decode(%q): %v", in, err)
		}
		if got != in {
			t.Errorf("round-trip mismatch:\n in=%q\nout=%q", in, got)
		}
	}
}

// TestAclItemArrayEmpty confirms an empty ACL ("{}" — an owner-side REVOKE that
// emptied the grants) encodes to a PG-valid empty _aclitem ArrayType (elemtype
// 1033, ndim 0) and decodes back to "{}".
func TestAclItemArrayEmpty(t *testing.T) {
	for _, in := range []string{"{}", ""} {
		blob, err := encodeAclItemArrayText(in, fixtureResolveOID)
		if err != nil {
			t.Fatalf("encode(%q): %v", in, err)
		}
		// Empty array: 16-byte header, ndim 0, elemtype 1033.
		if len(blob) != 16 {
			t.Errorf("encode(%q): empty array blob len = %d, want 16", in, len(blob))
		}
		if et := binary.LittleEndian.Uint32(blob[12:16]); et != 1033 {
			t.Errorf("encode(%q): elemtype = %d, want 1033", in, et)
		}
		if nd := binary.LittleEndian.Uint32(blob[4:8]); nd != 0 {
			t.Errorf("encode(%q): ndim = %d, want 0", in, nd)
		}
		got, err := decodeAclItemArrayText(blob, fixtureResolveName)
		if err != nil {
			t.Fatalf("decode(%q): %v", in, err)
		}
		if got != "{}" {
			t.Errorf("decode empty = %q, want \"{}\"", got)
		}
	}
}

// TestAclItemArrayQuotedRole verifies a role name that requires aclitemout
// quoting (uppercase / special chars) round-trips through aclPutid/aclUnputid
// and the top-level comma splitter (a quoted name containing a comma must not be
// torn apart).
func TestAclItemArrayQuotedRole(t *testing.T) {
	roleOIDFixture[`Weird,Role`] = 16400
	defer delete(roleOIDFixture, `Weird,Role`)

	in := `{"Weird,Role"=U/postgres,alice=U/postgres}`
	blob, err := encodeAclItemArrayText(in, fixtureResolveOID)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Two items must have been parsed despite the comma inside the quoted name.
	if n := binary.LittleEndian.Uint32(blob[16:20]); n != 2 {
		t.Fatalf("dims[0] = %d, want 2 (quoted comma split incorrectly)", n)
	}
	got, err := decodeAclItemArrayText(blob, fixtureResolveName)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != in {
		t.Errorf("quoted-role round-trip mismatch:\n in=%q\nout=%q", in, got)
	}
}
