// Package aclitem renders PostgreSQL's 16-byte AclItem (utils/acl.h:
// grantee oid, grantor oid, uint64 AclMode) as aclitemout does (acl.c). It is
// a leaf so the executor's heap codec and the pgoutput decoder
// (internal/access/transam/xlog, which cannot import the executor) share one
// renderer. M0149-0001k2.
package aclitem

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// Size is sizeof(AclItem).
const Size = 16

// rightsChars is ACL_ALL_RIGHTS_STR: privilege bit i is rightsChars[i].
const rightsChars = "arwdDxtXUCTcsAm"

// PrivLetters renders an AclMode as aclitemout's privilege string; a held
// grant option (bit i << 32) follows its letter as '*'.
func PrivLetters(mode uint64) string {
	priv, gopt := mode&0xFFFFFFFF, (mode>>32)&0xFFFFFFFF
	var sb strings.Builder
	for i := 0; i < len(rightsChars); i++ {
		bit := uint64(1) << uint(i)
		if priv&bit != 0 {
			sb.WriteByte(rightsChars[i])
			if gopt&bit != 0 {
				sb.WriteByte('*')
			}
		}
	}
	return sb.String()
}

// Putid is acl.c putid: a name of only [a-z0-9_] is bare, anything else is
// double-quoted with embedded quotes doubled; PUBLIC (empty) stays empty.
func Putid(name string) string {
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

// RoleName renders a role OID inside aclitem text: PUBLIC (0) is empty;
// otherwise the session's regrole renderer, unquoted so Putid can apply
// aclitemout's own quoting; with no renderer the bootstrap superuser by name
// and any other OID numerically, as aclitemout prints a role that no longer
// exists.
func RoleName(oid uint32, regOut func(typeName string, oid uint32) string) string {
	if oid == 0 {
		return ""
	}
	if regOut != nil {
		n := regOut("regrole", oid)
		if len(n) >= 2 && n[0] == '"' && n[len(n)-1] == '"' {
			n = strings.ReplaceAll(n[1:len(n)-1], `""`, `"`)
		}
		return n
	}
	if oid == 10 {
		return "postgres"
	}
	return strconv.FormatUint(uint64(oid), 10)
}

// Decode renders the AclItem at the head of data as aclitemout text.
func Decode(data []byte, regOut func(typeName string, oid uint32) string) (string, int, error) {
	if len(data) < Size {
		return "", 0, fmt.Errorf("truncated aclitem")
	}
	ge := binary.LittleEndian.Uint32(data[0:4])
	gr := binary.LittleEndian.Uint32(data[4:8])
	mode := binary.LittleEndian.Uint64(data[8:16])
	return Putid(RoleName(ge, regOut)) + "=" + PrivLetters(mode) + "/" + Putid(RoleName(gr, regOut)), Size, nil
}
