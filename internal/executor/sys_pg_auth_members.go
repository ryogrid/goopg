package executor

// B4.3 (docs/design/wal-pg-identical-stream/02d §3 B4): GRANT/REVOKE role
// membership journals real pg_auth_members heap rows (SHARED, global/1261),
// replacing the bespoke RecordKindGrantRoleMembership(79)/RevokeRoleMembership
// (80). One heap row per (roleid, member, grantor); goopg holds the membership
// (with its option flags) in the roleMembers registry, so any GRANT/REVOKE
// re-syncs the single affected row (stamp old + write current from the
// registry) — the B4.2 per-key re-sync pattern, which handles GRANT (upsert),
// REVOKE (row gone), REVOKE {ADMIN|INHERIT|SET} OPTION FOR (option cleared, row
// survives) and cascade (per dependent) uniformly.
//
// Emitted from the EXECUTOR layer (operators_ddl_role_membership.go) so it
// rides the session Context directly — no server-layer own-transaction dance.
// Like every non-boot-critical shared catalog in goopg (see
// bootstrapSharedCatalogPlaceholders), pg_auth_members' indexes (2694/2695/
// 6302/6303) are NOT materialized in global/, so PG reads it by seq scan and a
// heap INSERT/xmax-stamp alone is faithful — no runtime index maintenance.
// Column layout: postgres/src/include/catalog/pg_auth_members.h.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

const pgAuthMembersRelOID = 1261

// PGAuthMembersColumnsPG18 mirrors FormData_pg_auth_members (7 columns).
// Exported for the initdb reload.
func PGAuthMembersColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "roleid", Type: catalog.Type{Name: "oid"}},
		{Name: "member", Type: catalog.Type{Name: "oid"}},
		{Name: "grantor", Type: catalog.Type{Name: "oid"}},
		{Name: "admin_option", Type: catalog.Type{Name: "bool"}},
		{Name: "inherit_option", Type: catalog.Type{Name: "bool"}},
		{Name: "set_option", Type: catalog.Type{Name: "bool"}},
	}
}

func pgAuthMembersRel() storage.RelFileNode {
	// DBOid 0 → global/ (shared catalog); the B4.1a WAL encoder stamps the
	// block-ref locator with spcOid=1664/dbOid=0 for the standby.
	return storage.RelFileNode{DBOid: 0, RelOid: pgAuthMembersRelOID, Fork: storage.MainFork}
}

// syncAuthMemberRow re-syncs the single pg_auth_members row keyed by (roleid,
// member, grantor) from the post-mutation registry state: it stamps any live
// row for that key, then — if the membership still exists — writes its current
// options. Called after im.GrantRoleMembership / im.RevokeRoleMembership.
func syncAuthMemberRow(ctx *Context, im *catalog.InMemory, roleid, member, grantor uint32) error {
	if !catalogHeapSyncAvailable(ctx) {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	stampAuthMemberRow(ctx, roleid, member, grantor)
	m, ok := im.LookupRoleMembership(roleid, member, grantor)
	if !ok {
		return nil // REVOKE removed the row
	}
	row := Row{
		NewIntDatum(int64(m.OID)),
		NewIntDatum(int64(m.RoleOID)),
		NewIntDatum(int64(m.MemberOID)),
		NewIntDatum(int64(m.GrantorOID)),
		NewBoolDatum(m.AdminOption),
		NewBoolDatum(m.InheritOption),
		NewBoolDatum(m.SetOption),
	}
	if _, err := writeHeapRowCanonical(ctx, pgAuthMembersRel(), PGAuthMembersColumnsPG18(), row); err != nil {
		return err
	}
	return nil
}

// stampAuthMemberRow marks every live pg_auth_members row for the (roleid,
// member, grantor) key deleted. All columns are fixed-width non-null (4 oids +
// 3 bools), so the heap payload packs roleid@4 / member@8 / grantor@12. The
// caller has materialized the writer XID.
func stampAuthMemberRow(ctx *Context, roleid, member, grantor uint32) {
	stampCatalogRows(ctx, pgAuthMembersRel(), ctx.Tx.XID, func(data []byte) bool {
		return len(data) >= 16 &&
			binary.LittleEndian.Uint32(data[4:8]) == roleid &&
			binary.LittleEndian.Uint32(data[8:12]) == member &&
			binary.LittleEndian.Uint32(data[12:16]) == grantor
	})
}
