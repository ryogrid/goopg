package initdb

import "testing"

// TestPgAuthMembersRoleMemberIndexSeededFromInitialEntries pins Step 3z's
// catalog seed: pg_auth_members_role_member_index (OID 2694) must appear
// in pgIndexInitialEntries with the PG18-canonical 3-column indkey and
// uniqueness flags.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_auth_members.h:49
//	  DECLARE_UNIQUE_INDEX(pg_auth_members_role_member_index, 2694,
//	    AuthMemRoleMemIndexId, pg_auth_members,
//	    btree(roleid oid_ops, member oid_ops, grantor oid_ops));
//	  MAKE_SYSCACHE(AUTHMEMROLEMEM, pg_auth_members_role_member_index, 8);
//
// pg_auth_members column attnums (postgres/src/include/catalog/pg_auth_members_d.h):
//
//	1=oid, 2=roleid, 3=member, 4=grantor, 5=admin_option,
//	6=inherit_option, 7=set_option
//
// Without this entry RelationIdGetRelation(2694) FATALs with "could not
// open relation with OID 2694" — the Step 3z E2E failover boot blocker.
func TestPgAuthMembersRoleMemberIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 2694
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_auth_members_role_member_index) missing — Step 3z", oid)
	}
	if found.IndRelid != 1261 {
		t.Errorf("OID %d: IndRelid=%d, want 1261 (pg_auth_members heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{2, 3, 4}) {
		t.Errorf("OID %d: IndKey=%v, want [2 3 4] (roleid, member, grantor)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_UNIQUE_INDEX is NOT _PKEY; the PKEY of pg_auth_members is 6303)", oid)
	}
}

// TestNailedSharedRelsContainsPgAuthMembersRoleMemberIndex pins Step 3z's
// relcache nailed-rel seed: PG's RelationInitIndexAccessInfo
// relnatts/indnatts consistency check (relcache.c:1492) requires this
// entry in nailedSharedRels so the pg_class row's relnatts == 3.
// Shared because the parent pg_auth_members is BKI_SHARED_RELATION.
func TestNailedSharedRelsContainsPgAuthMembersRoleMemberIndex(t *testing.T) {
	const oid uint32 = 2694
	var found *nailedRel
	for i, r := range nailedSharedRels {
		if r.OID == oid {
			found = &nailedSharedRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedSharedRels: OID %d (pg_auth_members_role_member_index) missing — Step 3z", oid)
	}
	if found.RelName != "pg_auth_members_role_member_index" {
		t.Errorf("nailedSharedRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_auth_members_role_member_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedSharedRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 3 {
		t.Errorf("nailedSharedRels[%d] RelNatts=%d, want 3 (3-column composite btree)", oid, found.RelNatts)
	}
}
