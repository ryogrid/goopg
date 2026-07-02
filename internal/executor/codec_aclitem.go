package executor

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// codec_aclitem.go — PG-native `aclitem[]` (_aclitem, OID 1034) binary codec.
//
// The heap-backed pg_type catalog stores a user type's privileges in its
// `typacl` column as an on-disk `_aclitem` ArrayType varlena (M0097-0022 keeps
// pg_type rows PG18-canonical so a real PG18 standby attaching to a goopg
// basebackup reads them). The generic codec_array.go encoder treats an unknown
// element type as `text` (elemtype 25, 4-byte-varlena elements), which would
// store a `typacl` that a PG18 reader / pg_dump's getTypes mis-parses (it
// expects elemtype 1033 and 16-byte fixed AclItem structs). This file is the
// dedicated, PG-faithful aclitem array codec the heap re-sync path needs.
//
// Layout mirrors PostgreSQL's AclItem (src/include/utils/acl.h):
//
//	typedef struct AclItem {
//	    Oid     ai_grantee;  // 4 bytes — ACL_ID_PUBLIC (0) for a grant to PUBLIC
//	    Oid     ai_grantor;  // 4 bytes
//	    AclMode ai_privs;    // 8 bytes (uint64): low 32 bits = privilege bits,
//	                         //                   high 32 bits = grant-option bits
//	} AclItem;               // 16 bytes, typalign 'd' (8-byte)
//
// and the surrounding 1-D no-NULL ArrayType header is identical to the one
// codec_array.go writes (24-byte header: varlena + ndim + dataoffset + elemtype
// + dims[0] + lbound[0]); only the elemtype (1033) and the 16-byte fixed
// elements differ.
//
// The codec is pure: role name↔OID resolution is supplied by the caller so this
// file has no catalog dependency and is unit-testable in isolation. The GRANT
// heap re-sync path (M0119-0004-ACLHEAP, a later loop) supplies the resolvers
// from the per-role OID registry. Building block — no caller yet.

const (
	// aclItemSize is sizeof(AclItem): grantee(4) + grantor(4) + privs(8).
	aclItemSize = 16

	// aclItemElemTypeOID is pg_type.oid for the `aclitem` element type (1033);
	// the _aclitem ArrayType header stores it in its elemtype field so a PG18
	// reader parses the 16-byte elements correctly. acl is element 1033, the
	// array is 1034.
	aclItemElemTypeOID = 1033

	// aclPublicRoleOID is ACL_ID_PUBLIC (acl.h): the placeholder grantee OID for
	// a grant to the PUBLIC pseudo-role, rendered as the empty grantee by
	// aclitemout.
	aclPublicRoleOID = 0

	// aclGrantOptionShift is the bit offset of the grant-option half of an
	// AclMode (acl.h ACL_GRANT_OPTION_FOR: `(privs & 0xFFFFFFFF) << 32`). The low
	// 32 bits carry the privilege bits; bits 32..63 carry the matching
	// grant-option bits.
	aclGrantOptionShift = 32
)

// aclRightsChars is ACL_ALL_RIGHTS_STR (acl.h): the privilege letters in
// canonical aclitemout order. The letter at index i corresponds to AclMode
// privilege bit (1<<i) — 'a'=INSERT(0) … 'U'=USAGE(8) … 'm'=MAINTAIN(14).
const aclRightsChars = "arwdDxtXUCTcsAm"

// aclModeFromPrivLetters converts an aclitemout privilege string (e.g. "U",
// "rwU", "U*", "r*w") into an AclMode: each letter sets bit (1<<index), and a
// trailing '*' additionally sets the grant-option bit (the same bit shifted up
// by 32). Returns an error on an unrecognised privilege character.
func aclModeFromPrivLetters(privs string) (uint64, error) {
	var mode uint64
	for i := 0; i < len(privs); i++ {
		c := privs[i]
		idx := strings.IndexByte(aclRightsChars, c)
		if idx < 0 {
			return 0, fmt.Errorf("aclitem: unknown privilege char %q in %q", string(c), privs)
		}
		bit := uint64(1) << uint(idx)
		mode |= bit
		if i+1 < len(privs) && privs[i+1] == '*' {
			mode |= bit << aclGrantOptionShift
			i++ // consume the '*'
		}
	}
	return mode, nil
}

// aclModeToPrivLetters inverts aclModeFromPrivLetters: it renders an AclMode as
// the aclitemout privilege string, emitting each set privilege letter in
// canonical order, with a trailing '*' when the matching grant-option bit is
// also set.
func aclModeToPrivLetters(mode uint64) string {
	privBits := mode & 0xFFFFFFFF
	goptBits := (mode >> aclGrantOptionShift) & 0xFFFFFFFF
	var sb strings.Builder
	for i := 0; i < len(aclRightsChars); i++ {
		bit := uint64(1) << uint(i)
		if privBits&bit != 0 {
			sb.WriteByte(aclRightsChars[i])
			if goptBits&bit != 0 {
				sb.WriteByte('*')
			}
		}
	}
	return sb.String()
}

// aclPutid renders a role name as aclitemout does (PG's putid, acl.c): a name
// composed solely of lowercase letters, digits and '_' is emitted bare;
// otherwise it is wrapped in double quotes with embedded '"' doubled. The empty
// grantee (a grant to PUBLIC) is emitted bare (as the empty string), matching
// aclitemout's PUBLIC rendering.
func aclPutid(name string) string {
	if name == "" {
		return ""
	}
	safe := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' {
			safe = false
			break
		}
	}
	if safe {
		return name
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(name); i++ {
		if name[i] == '"' {
			sb.WriteByte('"')
		}
		sb.WriteByte(name[i])
	}
	sb.WriteByte('"')
	return sb.String()
}

// splitTopLevelAclItems splits the comma-separated body of an aclitem array
// (the text between the outer braces) into individual aclitem strings,
// respecting double-quoted role names so a quoted name containing a comma is
// not torn apart. Empty input yields a nil slice.
func splitTopLevelAclItems(body string) []string {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			cur.WriteByte(c)
		case c == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// aclUnputid reverses aclPutid for a single grantee/grantor token: a
// double-quoted token is unwrapped with doubled '"' collapsed; a bare token is
// returned verbatim.
func aclUnputid(tok string) string {
	tok = strings.TrimSpace(tok)
	if len(tok) >= 2 && tok[0] == '"' && tok[len(tok)-1] == '"' {
		inner := tok[1 : len(tok)-1]
		return strings.ReplaceAll(inner, `""`, `"`)
	}
	return tok
}

// parseAclItemText parses one aclitemout entry `grantee=privs/grantor` into its
// grantee name (empty == PUBLIC), AclMode and grantor name. The privilege list
// between '=' and the final '/' may be empty (a no-privilege entry). It locates
// the grantor at the LAST '/' so a (hypothetical) '/' inside a quoted role name
// is tolerated only outside quotes — for the canonical inputs goopg produces the
// names are simple identifiers.
func parseAclItemText(item string) (grantee string, mode uint64, grantor string, err error) {
	item = strings.TrimSpace(item)
	eq := indexTopLevelByte(item, '=')
	if eq < 0 {
		return "", 0, "", fmt.Errorf("aclitem: missing '=' in %q", item)
	}
	grantee = aclUnputid(item[:eq])
	rest := item[eq+1:]
	slash := lastIndexTopLevelByte(rest, '/')
	if slash < 0 {
		return "", 0, "", fmt.Errorf("aclitem: missing '/grantor' in %q", item)
	}
	privs := rest[:slash]
	grantor = aclUnputid(rest[slash+1:])
	mode, err = aclModeFromPrivLetters(privs)
	if err != nil {
		return "", 0, "", err
	}
	return grantee, mode, grantor, nil
}

// indexTopLevelByte returns the index of the first occurrence of b outside any
// double-quoted span, or -1.
func indexTopLevelByte(s string, b byte) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case b:
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// lastIndexTopLevelByte returns the index of the last occurrence of b outside
// any double-quoted span, or -1.
func lastIndexTopLevelByte(s string, b byte) int {
	inQuote := false
	last := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case b:
			if !inQuote {
				last = i
			}
		}
	}
	return last
}

// encodeAclItemArrayText converts the canonical aclitemout array text produced
// by the catalog ACL renderer (e.g. "{postgres=U/postgres,=U/postgres,
// g=U*/postgres}") into a PG-native _aclitem ArrayType varlena blob: a 24-byte
// 1-D no-NULL header (elemtype 1033) followed by one 16-byte AclItem per entry.
//
// resolveOID maps a role name to its OID; the empty grantee (a grant to PUBLIC)
// resolves to ACL_ID_PUBLIC (0) without consulting resolveOID. The resulting
// blob is byte-identical to what PG's array_out / aclitemout would round-trip,
// so a PG18 standby and pg_dump's getTypes read a goopg-written typacl
// correctly.
//
// An empty array text ("{}" or "") yields an empty _aclitem ArrayType
// (elemtype 1033, ndim 0) so an owner-side REVOKE that empties the ACL still
// stores a PG-valid value.
func encodeAclItemArrayText(aclText string, resolveOID func(roleName string) uint32) ([]byte, error) {
	body := strings.TrimSpace(aclText)
	body = strings.TrimPrefix(body, "{")
	body = strings.TrimSuffix(body, "}")
	items := splitTopLevelAclItems(body)
	if len(items) == 0 {
		return emptyArrayTypeBytes(aclItemElemTypeOID), nil
	}

	elems := make([]byte, 0, len(items)*aclItemSize)
	for _, it := range items {
		grantee, mode, grantor, err := parseAclItemText(it)
		if err != nil {
			return nil, err
		}
		var granteeOID uint32
		if grantee == "" {
			granteeOID = aclPublicRoleOID
		} else {
			granteeOID = resolveOID(grantee)
		}
		grantorOID := resolveOID(grantor)
		var ai [aclItemSize]byte
		binary.LittleEndian.PutUint32(ai[0:4], granteeOID)
		binary.LittleEndian.PutUint32(ai[4:8], grantorOID)
		binary.LittleEndian.PutUint64(ai[8:16], mode)
		elems = append(elems, ai[:]...)
	}

	total := arrayHeaderSize + len(elems)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2) // varlena header
	binary.LittleEndian.PutUint32(buf[4:8], 1)                // ndim
	binary.LittleEndian.PutUint32(buf[8:12], 0)               // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(buf[12:16], aclItemElemTypeOID)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(items))) // dims[0]
	binary.LittleEndian.PutUint32(buf[20:24], 1)                  // lbound[0]
	copy(buf[arrayHeaderSize:], elems)
	return buf, nil
}

// decodeAclItemArrayText inverts encodeAclItemArrayText: it reads an _aclitem
// ArrayType varlena blob (with its 4-byte varlena header) and renders the
// canonical aclitemout array text "{grantee=privs/grantor,...}". resolveName
// maps a role OID back to its name; ACL_ID_PUBLIC (0) renders as the empty
// grantee regardless of resolveName. A 0-dimension array yields "{}".
func decodeAclItemArrayText(blob []byte, resolveName func(oid uint32) string) (string, error) {
	if len(blob) < arrayHeaderSize {
		// Too short to carry a header; treat as empty (matches the ndim==0 case).
		return "{}", nil
	}
	ndim := binary.LittleEndian.Uint32(blob[4:8])
	if ndim == 0 {
		return "{}", nil
	}
	n := int(binary.LittleEndian.Uint32(blob[16:20]))
	if n <= 0 {
		return "{}", nil
	}
	data := blob[arrayHeaderSize:]
	if len(data) < n*aclItemSize {
		return "", fmt.Errorf("aclitem: truncated array body (have %d bytes, need %d for %d items)",
			len(data), n*aclItemSize, n)
	}
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i < n; i++ {
		off := i * aclItemSize
		granteeOID := binary.LittleEndian.Uint32(data[off : off+4])
		grantorOID := binary.LittleEndian.Uint32(data[off+4 : off+8])
		mode := binary.LittleEndian.Uint64(data[off+8 : off+16])
		if i > 0 {
			sb.WriteByte(',')
		}
		granteeName := ""
		if granteeOID != aclPublicRoleOID {
			granteeName = resolveName(granteeOID)
		}
		sb.WriteString(aclPutid(granteeName))
		sb.WriteByte('=')
		sb.WriteString(aclModeToPrivLetters(mode))
		sb.WriteByte('/')
		sb.WriteString(aclPutid(resolveName(grantorOID)))
	}
	sb.WriteByte('}')
	return sb.String(), nil
}
