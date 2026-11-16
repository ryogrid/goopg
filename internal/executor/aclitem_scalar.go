package executor

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/utils/adt/aclitem"
)

// Scalar aclitem columns (M0149-0001k2). PG stores an aclitem value as the
// 16-byte AclItem struct (utils/acl.h: grantee oid, grantor oid, uint64
// AclMode; typlen 16, typalign 'd'). goopg keeps the in-memory Datum as
// aclitemout's text and converts at the heap boundary, in a cluster with the
// fixed_length_aclitem capability. The element layout and the privilege
// letters are the ones the catalog aclitem[] codec (codec_aclitem.go) already
// writes.

// encodeAclItemScalar is aclitemin's storage half: parse
// "grantee=privs/grantor" and resolve both role names through the session
// catalog. An unknown role is 42704, as get_role_oid reports it; without a
// catalog only the bootstrap superuser resolves.
func encodeAclItemScalar(text string, ctx *Context) ([]byte, error) {
	grantee, mode, grantor, err := parseAclItemText(text)
	if err != nil {
		return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type aclitem: \"%s\"", text)}
	}
	resolve := func(name string) (uint32, error) {
		if name == "" {
			return aclPublicRoleOID, nil
		}
		if ctx != nil && ctx.Catalog != nil {
			if oid, ok := ctx.Catalog.RoleOID(name); ok {
				return oid, nil
			}
		} else if strings.EqualFold(name, "postgres") {
			return 10, nil
		}
		return 0, &ExecError{Code: "42704", Message: fmt.Sprintf("role \"%s\" does not exist", name)}
	}
	ge, err := resolve(grantee)
	if err != nil {
		return nil, err
	}
	gr, err := resolve(grantor)
	if err != nil {
		return nil, err
	}
	b := make([]byte, aclItemSize)
	binary.LittleEndian.PutUint32(b[0:4], ge)
	binary.LittleEndian.PutUint32(b[4:8], gr)
	binary.LittleEndian.PutUint64(b[8:16], mode)
	return b, nil
}

// DecodeAclItemScalar renders a 16-byte AclItem as aclitemout does (the
// shared leaf renderer, also used by pgoutput).
func DecodeAclItemScalar(data []byte, regOut func(typeName string, oid uint32) string) (string, int, error) {
	return aclitem.Decode(data, regOut)
}
