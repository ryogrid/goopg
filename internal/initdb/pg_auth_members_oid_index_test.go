package initdb

import "testing"

// TestPgAuthMembersOidIndexSeededFromInitialEntries pins batched-13's catalog
// seed: pg_auth_members_oid_index (OID 6303) must appear in
// pgIndexInitialEntries with the PG18-canonical 1-column indkey and
// uniqueness/primary flags.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_auth_members.h:48
//	  DECLARE_UNIQUE_INDEX_PKEY(pg_auth_members_oid_index, 6303,
//	    AuthMemOidIndexId, pg_auth_members, btree(oid oid_ops));
//
// pg_auth_members column attnums (postgres/src/include/catalog/pg_auth_members_d.h):
//
//	1=oid, 2=roleid, 3=member, 4=grantor, 5=admin_option,
//	6=inherit_option, 7=set_option
func TestPgAuthMembersOidIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 6303
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_auth_members_oid_index) missing — batched-13", oid)
	}
	if found.IndRelid != 1261 {
		t.Errorf("OID %d: IndRelid=%d, want 1261 (pg_auth_members heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{1}) {
		t.Errorf("OID %d: IndKey=%v, want [1] (oid)", oid, found.IndKey)
	}
	if !found.IsUnique {
		t.Errorf("OID %d: IsUnique=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
	if !found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=false, want true (DECLARE_UNIQUE_INDEX_PKEY)", oid)
	}
}

// TestNailedSharedRelsContainsPgAuthMembersOidIndex pins batched-13's
// relcache nailed-rel seed: PG's RelationInitIndexAccessInfo
// relnatts/indnatts consistency check requires this entry in nailedSharedRels
// so the pg_class row's relnatts == 1.
func TestNailedSharedRelsContainsPgAuthMembersOidIndex(t *testing.T) {
	const oid uint32 = 6303
	var found *nailedRel
	for i, r := range nailedSharedRels {
		if r.OID == oid {
			found = &nailedSharedRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedSharedRels: OID %d (pg_auth_members_oid_index) missing — batched-13", oid)
	}
	if found.RelName != "pg_auth_members_oid_index" {
		t.Errorf("nailedSharedRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_auth_members_oid_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedSharedRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedSharedRels[%d] RelNatts=%d, want 1 (1-column btree on oid)", oid, found.RelNatts)
	}
}
