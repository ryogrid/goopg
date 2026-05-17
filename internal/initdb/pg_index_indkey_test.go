package initdb

import "testing"

// TestPgIndexInitialEntriesIndkeyMatchesPG18 pins the M0106-0010 Step 3n
// fix: every pgIndexInitialEntries row's IndKey must list the PG18 heap
// attnums of the indexed columns, because PG's
// systable_beginscan() (postgres/src/backend/access/index/genam.c:437–446)
// walks irel->rd_index->indkey searching for the caller's sk_attno (a
// heap attnum derived from Anum_pg_*_* constants) and FATALs with
// "column is not in index" on mismatch. Before Step 3n four entries had
// stale legacy attnums:
//
//	2659 pg_attribute_relid_attnum_index : [1,6] → [1,5]  (PG18 attnum=col 5)
//	2693 pg_rewrite_rel_rulename_index   : [2,7] → [3,2]  (ev_class=3, rulename=2)
//	2701 pg_trigger_tgrelid_tgname_index : [2,3] → [2,4]  (tgname=col 4)
//	3593 pg_shseclabel_object_index      : [3,2,5] → [1,2,3] (objoid,classoid,provider)
//
// Each expected vector below is the authoritative PG18 layout taken
// from postgres/src/include/catalog/pg_*.h. Add new pinned indexes here
// whenever pgIndexInitialEntries grows.
func TestPgIndexInitialEntriesIndkeyMatchesPG18(t *testing.T) {
	want := map[uint32][]int16{
		// Shared catalogs.
		2671: {2},       // pg_database_datname_index  : btree(datname)
		2672: {1},       // pg_database_oid_index      : btree(oid)
		2676: {2},       // pg_authid_rolname_index    : btree(rolname)
		2677: {1},       // pg_authid_oid_index        : btree(oid)
		2694: {2, 3, 4}, // pg_auth_members_role_member_index : btree(roleid, member, grantor) UNIQUE ← Step 3z
		2695: {3, 2, 4}, // pg_auth_members_member_role_index : btree(member, roleid, grantor)
		3593: {1, 2, 3}, // pg_shseclabel_object_index : btree(objoid, classoid, provider)
		// Local catalogs.
		2703: {1},        // pg_type_oid_index
		2704: {2, 3},     // pg_type_typname_nsp_index    : btree(typname, typnamespace)
		2658: {1, 2},     // pg_attribute_relid_attnam_index
		2659: {1, 5},     // pg_attribute_relid_attnum_index : btree(attrelid, attnum)  ← Step 3n
		2662: {1},        // pg_class_oid_index
		2663: {2, 3},     // pg_class_relname_nsp_index
		2690: {1},        // pg_proc_oid_index
		2691: {2, 20, 3}, // pg_proc_proname_args_nsp_index : btree(proname, proargtypes, pronamespace)
		2678: {2},        // pg_index_indrelid_index   : btree(indrelid)     NON-UNIQUE      ← Step 3r
		2679: {1},        // pg_index_indexrelid_index : btree(indexrelid)   UNIQUE PRIMARY ← Step 3r
		2687: {1},        // pg_opclass_oid_index
		2655: {2, 3, 4, 5}, // pg_amproc_fam_proc_index : btree(amprocfamily, lefttype, righttype, amprocnum)
		2693: {3, 2},     // pg_rewrite_rel_rulename_index : btree(ev_class, rulename)  ← Step 3n
		2701: {2, 4},     // pg_trigger_tgrelid_tgname_index : btree(tgrelid, tgname)   ← Step 3n
		2667: {1},        // pg_constraint_oid_index
		2688: {1},        // pg_operator_oid_index
		2680: {1, 3},     // pg_inherits_relid_seqno_index : btree(inhrelid, inhseqno)
		2684: {2},        // pg_namespace_nspname_index : btree(nspname name_ops)            ← Step 3t
		2685: {1},        // pg_namespace_oid_index     : btree(oid oid_ops) UNIQUE PRIMARY ← Step 3t
		2654: {7, 6, 2},  // pg_amop_opr_fam_index : btree(amopopr, amoppurpose, amopfamily)
		2650: {1},        // pg_aggregate_fnoid_index : btree(aggfnoid oid_ops) UNIQUE PRIMARY ← Step 3x
		2653: {2, 3, 4, 5}, // pg_amop_fam_strat_index : btree(amopfamily, amoplefttype, amoprighttype, amopstrategy) UNIQUE ← Step 3y
		2660: {1},        // pg_cast_oid_index : btree(oid oid_ops) UNIQUE PRIMARY ← Step 3ab
		2661: {2, 3},     // pg_cast_source_target_index : btree(castsource oid_ops, casttarget oid_ops) UNIQUE ← Step 3ac
		2686: {2, 3, 4}, // pg_opclass_am_name_nsp_index : btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops) UNIQUE ← Step 3ad
		3164: {2, 7, 3}, // pg_collation_name_enc_nsp_index : btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops) UNIQUE ← Step 3ae
		3085: {1},       // pg_collation_oid_index : btree(oid oid_ops) UNIQUE PRIMARY ← Step 3af
		2668: {3, 5, 6, 1}, // pg_conversion_default_index : btree(connamespace oid_ops, conforencoding int4_ops, contoencoding int4_ops, oid oid_ops) UNIQUE ← Step 3ah
		2670: {1},          // pg_conversion_oid_index : btree(oid oid_ops) UNIQUE PRIMARY ← Step 3ai
		2669: {2, 3},       // pg_conversion_name_nsp_index : btree(conname name_ops, connamespace oid_ops) UNIQUE ← Step 3aj
		827: {2, 3, 4},    // pg_default_acl_role_nsp_obj_index : btree(defaclrole oid_ops, defaclnamespace oid_ops, defaclobjtype char_ops) UNIQUE ← Step 3al
	}
	got := make(map[uint32][]int16, len(want))
	for _, e := range pgIndexInitialEntries() {
		got[e.IndexRelid] = e.IndKey
	}
	if len(got) != len(want) {
		t.Fatalf("pgIndexInitialEntries: %d entries, want %d (extend the pinned map below to cover new indexes)", len(got), len(want))
	}
	for oid, expect := range want {
		actual, ok := got[oid]
		if !ok {
			t.Errorf("index OID %d: missing from pgIndexInitialEntries", oid)
			continue
		}
		if !int16SliceEqual(actual, expect) {
			t.Errorf("index OID %d: indkey=%v, want %v (PG18 heap attnums)", oid, actual, expect)
		}
	}
}

func int16SliceEqual(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
