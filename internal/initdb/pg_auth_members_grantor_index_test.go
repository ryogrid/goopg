package initdb

import "testing"

// TestPgAuthMembersGrantorIndexSeededFromInitialEntries pins batched-13's
// catalog seed: pg_auth_members_grantor_index (OID 6302) must appear in
// pgIndexInitialEntries with the PG18-canonical 1-column indkey and
// non-unique, non-primary flags.
//
// Authoritative source:
//
//	postgres/src/include/catalog/pg_auth_members.h:51
//	  DECLARE_INDEX(pg_auth_members_grantor_index, 6302,
//	    AuthMemGrantorIndexId, pg_auth_members, btree(grantor oid_ops));
//
// pg_auth_members column attnums (postgres/src/include/catalog/pg_auth_members_d.h):
//
//	1=oid, 2=roleid, 3=member, 4=grantor, 5=admin_option,
//	6=inherit_option, 7=set_option
func TestPgAuthMembersGrantorIndexSeededFromInitialEntries(t *testing.T) {
	const oid uint32 = 6302
	var found *pgIndexEntry
	for i, e := range pgIndexInitialEntries() {
		if e.IndexRelid == oid {
			found = &pgIndexInitialEntries()[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pgIndexInitialEntries: OID %d (pg_auth_members_grantor_index) missing — batched-13", oid)
	}
	if found.IndRelid != 1261 {
		t.Errorf("OID %d: IndRelid=%d, want 1261 (pg_auth_members heap OID)", oid, found.IndRelid)
	}
	if !int16SliceEqual(found.IndKey, []int16{4}) {
		t.Errorf("OID %d: IndKey=%v, want [4] (grantor)", oid, found.IndKey)
	}
	if found.IsUnique {
		t.Errorf("OID %d: IsUnique=true, want false (DECLARE_INDEX is non-unique)", oid)
	}
	if found.IsPrimary {
		t.Errorf("OID %d: IsPrimary=true, want false (DECLARE_INDEX is non-primary)", oid)
	}
}

// TestNailedSharedRelsContainsPgAuthMembersGrantorIndex pins batched-13's
// relcache nailed-rel seed: PG's RelationInitIndexAccessInfo
// relnatts/indnatts consistency check requires this entry in nailedSharedRels
// so the pg_class row's relnatts == 1.
func TestNailedSharedRelsContainsPgAuthMembersGrantorIndex(t *testing.T) {
	const oid uint32 = 6302
	var found *nailedRel
	for i, r := range nailedSharedRels {
		if r.OID == oid {
			found = &nailedSharedRels[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("nailedSharedRels: OID %d (pg_auth_members_grantor_index) missing — batched-13", oid)
	}
	if found.RelName != "pg_auth_members_grantor_index" {
		t.Errorf("nailedSharedRels[%d] RelName=%q, want %q", oid, found.RelName, "pg_auth_members_grantor_index")
	}
	if found.RelKind != 'i' {
		t.Errorf("nailedSharedRels[%d] RelKind=%q, want 'i'", oid, found.RelKind)
	}
	if found.RelNatts != 1 {
		t.Errorf("nailedSharedRels[%d] RelNatts=%d, want 1 (1-column btree on grantor)", oid, found.RelNatts)
	}
}
