package executor

// aclitem input (aclitemin / aclparse / getid, utils/adt/acl.c) and
// makeaclitem. goopg keeps an aclitem Datum as aclitemout's text, so the
// input function validates and canonicalises: names are resolved to roles
// (and re-rendered from the role's stored name), the privilege letters are
// re-emitted in aclitemout order. M0149-0001h1a9.

import (
	"fmt"
	"strconv"
	"strings"
)

// aclAllRightsStr is ACL_ALL_RIGHTS_STR.
const aclAllRightsStr = "arwdDxtXUCTcsAm"

// aclSafeChar is is_safe_acl_char(c, true): a high-bit byte, an
// alphanumeric or '_'.
func aclSafeChar(c byte) bool {
	return c >= 0x80 || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func aclIsSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// aclGetid is getid: skip spaces, read a (possibly double-quoted) name,
// skip trailing spaces; returns the name and the remaining input.
func aclGetid(s string) (string, string, *ExecError) {
	i := 0
	for i < len(s) && aclIsSpace(s[i]) {
		i++
	}
	var name []byte
	inQuotes := false
	for i < len(s) && (inQuotes || s[i] == '"' || aclSafeChar(s[i])) {
		if s[i] == '"' {
			if !inQuotes {
				inQuotes = true
				i++
				continue
			}
			if i+1 >= len(s) || s[i+1] != '"' {
				inQuotes = false
				i++
				continue
			}
			i++ // escaped double quote: keep one
		}
		if len(name) >= 63 {
			return "", "", &ExecError{Code: "42622", Message: "identifier too long",
				Detail: "Identifier must be less than 64 characters."}
		}
		name = append(name, s[i])
		i++
	}
	for i < len(s) && aclIsSpace(s[i]) {
		i++
	}
	return string(name), s[i:], nil
}

// aclitemIn is aclitemin: it returns the canonical aclitemout text of s,
// or the error PG raises (soft errors in pg_input_is_valid / _error_info).
// A missing "/grantor" defaults the grantor to the bootstrap superuser with
// a WARNING, as aclparse does.
func aclitemIn(s string, ctx *Context) (string, *ExecError) {
	invalid := func(msg, hint string) *ExecError {
		return &ExecError{Code: "22P02", Message: msg, Hint: hint}
	}
	name, rest, err := aclGetid(s)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(rest, "=") {
		// We just read a keyword, not a name.
		if name != "group" && name != "user" {
			return "", invalid(fmt.Sprintf("unrecognized key word: \"%s\"", name), "ACL key word must be \"group\" or \"user\".")
		}
		if name, rest, err = aclGetid(rest); err != nil {
			return "", err
		}
		if name == "" {
			return "", invalid("missing name", "A name must follow the \"group\" or \"user\" key word.")
		}
	}
	if !strings.HasPrefix(rest, "=") {
		return "", invalid("missing \"=\" sign", "")
	}
	rest = rest[1:]
	var privs, goptions, read uint64
	i := 0
	for ; i < len(rest) && (rest[i] == '*' || (rest[i] >= 'a' && rest[i] <= 'z') || (rest[i] >= 'A' && rest[i] <= 'Z')); i++ {
		if rest[i] == '*' {
			goptions |= read
			continue
		}
		idx := strings.IndexByte(aclRightsChars, rest[i])
		if idx < 0 {
			return "", invalid(fmt.Sprintf("invalid mode character: must be one of \"%s\"", aclAllRightsStr), "")
		}
		read = uint64(1) << uint(idx)
		privs |= read
	}
	rest = rest[i:]
	roleName := func(n string) (string, *ExecError) {
		if ctx == nil || ctx.Catalog == nil {
			if n == "postgres" {
				return n, nil
			}
		} else if oid, ok := ctx.Catalog.RoleOID(n); ok {
			if rn := roleNameForACL(ctx, oid); rn != "" {
				return rn, nil
			}
			return n, nil
		}
		return "", &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", n)}
	}
	grantee := ""
	if name != "" {
		g, err := roleName(name)
		if err != nil {
			return "", err
		}
		grantee = g
	}
	var grantor string
	if strings.HasPrefix(rest, "/") {
		var n2 string
		if n2, rest, err = aclGetid(rest[1:]); err != nil {
			return "", err
		}
		if n2 == "" {
			return "", invalid("a name must follow the \"/\" sign", "")
		}
		if grantor, err = roleName(n2); err != nil {
			return "", err
		}
	} else {
		grantor = roleNameForACL(ctx, 10)
		if grantor == "" {
			grantor = "postgres"
		}
		if ctx != nil {
			ctx.AddWarning("defaulting grantor to user ID 10")
		}
	}
	for len(rest) > 0 && aclIsSpace(rest[0]) {
		rest = rest[1:]
	}
	if rest != "" {
		return "", invalid("extra garbage at the end of the ACL specification", "")
	}
	mode := privs | goptions<<aclGrantOptionShift
	gtext := ""
	if grantee != "" {
		gtext = aclPutid(grantee)
	}
	return gtext + "=" + aclModeToPrivLetters(mode) + "/" + aclPutid(grantor), nil
}

// roleNameForACL is the role's stored name for oid ("" when unknown).
func roleNameForACL(ctx *Context, oid uint32) string {
	if ctx == nil || ctx.Catalog == nil {
		if oid == 10 {
			return "postgres"
		}
		return ""
	}
	type namer interface{ RoleNameForOID(uint32) string }
	if n, ok := ctx.Catalog.(namer); ok {
		return n.RoleNameForOID(oid)
	}
	return ""
}

// makeaclitemPrivs is makeaclitem's any_priv_map (acl.c), name → letter.
var makeaclitemPrivs = []struct {
	name   string
	letter byte
}{
	{"SELECT", 'r'}, {"INSERT", 'a'}, {"UPDATE", 'w'}, {"DELETE", 'd'},
	{"TRUNCATE", 'D'}, {"REFERENCES", 'x'}, {"TRIGGER", 't'}, {"EXECUTE", 'X'},
	{"USAGE", 'U'}, {"CREATE", 'C'}, {"TEMP", 'T'}, {"TEMPORARY", 'T'},
	{"CONNECT", 'c'}, {"SET", 's'}, {"ALTER SYSTEM", 'A'}, {"MAINTAIN", 'm'},
}

// evalMakeAclItem is makeaclitem(grantee oid, grantor oid, privileges text,
// is_grantable bool): convert_any_priv_string over a comma-separated list
// (22023 "unrecognized privilege type"), the grant option on every privilege
// when is_grantable. Grantee 0 is PUBLIC; a role OID with no role renders as
// its number, as aclitemout's putid fallback does.
func evalMakeAclItem(args []Datum, ctx *Context, pos int) (Datum, error) {
	if len(args) != 4 {
		return Datum{}, &ExecError{Code: "42883", Pos: pos, Message: "function makeaclitem requires 4 arguments"}
	}
	for _, a := range args {
		if a.IsNull() {
			return NullDatum, nil
		}
	}
	var privs uint64
	for _, chunk := range strings.Split(args[2].StringValue(), ",") {
		chunk = strings.TrimSpace(chunk)
		found := false
		for _, p := range makeaclitemPrivs {
			if strings.EqualFold(p.name, chunk) {
				privs |= uint64(1) << uint(strings.IndexByte(aclRightsChars, p.letter))
				found = true
				break
			}
		}
		if !found {
			return Datum{}, &ExecError{Code: "22023", Message: fmt.Sprintf("unrecognized privilege type: \"%s\"", chunk)}
		}
	}
	mode := privs
	if args[3].BoolValue() {
		mode |= privs << aclGrantOptionShift
	}
	render := func(oid uint32) string {
		if n := roleNameForACL(ctx, oid); n != "" {
			return aclPutid(n)
		}
		return strconv.FormatUint(uint64(oid), 10)
	}
	// A regrole argument can reach here as the role's name rather than its
	// OID; resolve it the way regrolein would.
	roleOID := func(d Datum) (uint32, error) {
		if d.Kind != KindString {
			return uint32(d.Int), nil
		}
		txt := strings.TrimSpace(d.StringValue())
		if n, err := strconv.ParseUint(txt, 10, 32); err == nil {
			return uint32(n), nil
		}
		if ctx != nil && ctx.Catalog != nil {
			if oid, ok := ctx.Catalog.RoleOID(txt); ok {
				return oid, nil
			}
		}
		return 0, &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", txt)}
	}
	ge, err := roleOID(args[0])
	if err != nil {
		return Datum{}, err
	}
	gr, err := roleOID(args[1])
	if err != nil {
		return Datum{}, err
	}
	grantee := ""
	if ge != 0 {
		grantee = render(ge)
	}
	return NewStringDatum(grantee + "=" + aclModeToPrivLetters(mode) + "/" + render(gr)), nil
}
