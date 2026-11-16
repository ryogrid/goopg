package catalog

import (
	"strconv"
	"strings"
)

// InitdbInitPriv is one pg_init_privs row a PG 18.3 initdb records
// (FormData_pg_init_privs, postgres/src/include/catalog/pg_init_privs.h).
// InitPrivs is aclitem[] text with the bootstrap superuser spelled "B".
type InitdbInitPriv struct {
	ObjOID    uint32
	ClassOID  uint32
	ObjSubID  int32
	PrivType  byte
	InitPrivs string
}

// PGInitPrivsRowsForDBOid renders pg_init_privs as a PG 18.3 initdb leaves it:
// setup_privileges() (postgres/src/bin/initdb/initdb.c) snapshots the ACL of
// every system relation, column, function and namespace whose ACL is non-NULL
// once system_views.sql / system_functions.sql have run its GRANTs and
// REVOKEs. The rows come from PGInitdbInitPrivs (scripts/gen-pg-init-privs.sh),
// the same source the bootstrap heap 3394 is written from (M0149-0001g slice
// 22), so goopg's SQL view and the on-disk catalog a PG node reads agree. The
// bootstrap superuser renders under its current name. Rows are not maintained
// afterwards: goopg has no CREATE EXTENSION path that records new initprivs
// (PG's recordExtensionInitPriv), and a later GRANT on a system object leaves
// pg_init_privs unchanged in PG as well. Caller must NOT hold c.mu.
func (c *InMemory) PGInitPrivsRowsForDBOid(dbOid uint32) [][]string {
	_ = dbOid // every database is a copy of template1 and carries the same rows
	su := c.RoleNameForOID(10)
	out := make([][]string, 0, len(PGInitdbInitPrivs))
	for _, r := range PGInitdbInitPrivs {
		out = append(out, []string{
			strconv.FormatUint(uint64(r.ObjOID), 10),
			strconv.FormatUint(uint64(r.ClassOID), 10),
			strconv.FormatInt(int64(r.ObjSubID), 10),
			string(r.PrivType),
			InitdbACLText(r.InitPrivs, su),
		})
	}
	return out
}

// InitdbACLText replaces the "B" placeholder role of a generated initdb ACL
// (grantee before '=', grantor after '/') with the bootstrap superuser's name.
func InitdbACLText(acl, superuser string) string {
	body := strings.TrimSuffix(strings.TrimPrefix(acl, "{"), "}")
	if body == "" {
		return acl
	}
	items := strings.Split(body, ",")
	for i, it := range items {
		eq := strings.IndexByte(it, '=')
		sl := strings.LastIndexByte(it, '/')
		if eq < 0 || sl < eq {
			continue
		}
		grantee, privs, grantor := it[:eq], it[eq+1:sl], it[sl+1:]
		if grantee == "B" {
			grantee = superuser
		}
		if grantor == "B" {
			grantor = superuser
		}
		items[i] = grantee + "=" + privs + "/" + grantor
	}
	return "{" + strings.Join(items, ",") + "}"
}
