package initdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// relCacheInitFileMagic is PG's RELCACHE_INIT_FILEMAGIC (0x573266).
const relCacheInitFileMagic = 0x573266

// PG18 struct sizes verified from DWARF (readelf -wi postgres):
const (
	sizeofRelationData      = 488
	sizeofFormDataPgClass   = 144
	attrFixedPartSize        = 100 // ATTRIBUTE_FIXED_PART_SIZE
	pgNameDataLen            = 64  // NAMEDATALEN
)

// bootstrapRelcacheInitFiles generates PG-compatible relcache init files.
// PG's load_relcache_init_file() reads these binary files to populate the
// relation cache for nailed system catalogs and their indexes. Without
// them, backend startup PANICs on "could not open critical system index".
func bootstrapRelcacheInitFiles(dataDir string) error {
	// Shared init file (global/pg_internal.init)
	if err := writeRelcacheInitFile(dataDir, true, nailedSharedRels); err != nil {
		return fmt.Errorf("shared relcache init: %w", err)
	}
	// Local init file (base/1/pg_internal.init — goopg default DB)
	if err := writeRelcacheInitFile(dataDir, false, nailedLocalRels); err != nil {
		return fmt.Errorf("local relcache init (base/1): %w", err)
	}
	// Copy to base/5/ for the "postgres" database (same nailed local rels).
	src := filepath.Join(dataDir, "base", "1", "pg_internal.init")
	dst := filepath.Join(dataDir, "base", "5", "pg_internal.init")
	srcData, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read base/1/pg_internal.init: %w", err)
	}
	if err := os.WriteFile(dst, srcData, 0o600); err != nil {
		return fmt.Errorf("write base/5/pg_internal.init: %w", err)
	}
	// No chmod — PG must be able to unlink and rewrite the file when a
	// nailed-rel DDL transaction commits (RelationCacheInitFilePreInvalidate).
	return nil
}

// nailedRel describes one nailed relation or index for the init file.
type nailedRel struct {
	OID      uint32 // relation OID
	RelName  string
	RelType  uint32 // rowtype OID (for pg_type entry)
	RelKind  byte   // 'r'=heap, 'i'=index
	RelNatts int16  // number of attributes
	IsShared bool
	Attrs    []nailedAttr
}

type nailedAttr struct {
	Name      string
	TypeOID   uint32
	Num       int16
	Len       int16
	NotNull   bool
	IsDropped bool
}

// nailedSharedRels lists all shared nailed relations (heaps + indexes flattened).
var nailedSharedRels = flattenRels([]nailedRel{
	{1262, "pg_database", 1248, 'r', 18, true, pgDatabaseAttrs()},
	{1260, "pg_authid", 2842, 'r', 12, true, pgAuthidAttrs()},
	{1261, "pg_auth_members", 2843, 'r', 5, true, pgAuthMembersAttrs()},
	// pg_shseclabel reltype must equal SharedSecLabelRelation_Rowtype_Id
	// (4066) from postgres/src/include/catalog/pg_shseclabel_d.h. PG's
	// formrdesc("pg_shseclabel", ...) call in
	// RelationCacheInitializePhase2 hardcodes tdtypeid=4066; if our heap
	// row's reltype disagrees, the Phase3 assertion
	// `relation->rd_att->tdtypeid == relp->reltype` (relcache.c:4293)
	// PANICs every connecting client backend. See
	// docs/design/0106-0010-step3v-pg-shseclabel-reltype.md.
	{3592, "pg_shseclabel", 4066, 'r', 4, true, pgShseclabelAttrs()},
	// relnatts 9 → 18 (M0131-S6): Natts_pg_subscription is 18 and goopg's
	// own writer has always emitted 18 columns; see pgSubscriptionAttrs.
	{6100, "pg_subscription", 6101, 'r', 18, true, pgSubscriptionAttrs()},
	// M0106-0010 step 3bp: pg_parameter_acl is opened during PG-standby
	// boot once Step 3bo cleared the pg_opfamily family. Without a
	// pg_class row, `RelationBuildDesc(6243) → ScanPgRelation(6243)`
	// returns NULL and every forked backend FATALs with
	// `could not open relation with OID 6243`. RelType=83 is safe because
	// pg_parameter_acl is not formrdesc'd (no
	// `ParameterAclRelation_Rowtype_Id` constant in PG18 headers; only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`), so the
	// Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion
	// (relcache.c:4293) does not fire. Schema per
	// `postgres/src/include/catalog/pg_parameter_acl.h` (PG18, 3 cols:
	// oid/parname/paracl). The empty 8 KiB heap at `global/6243` is
	// already produced by `bootstrapSharedCatalogPlaceholders`.
	// Companion indexes 6246 (`pg_parameter_acl_parname_index`) and
	// 6247 (`pg_parameter_acl_oid_index`) are deferred to follow-up
	// steps — the Step-3k empty btree placeholders are sufficient
	// because pg_parameter_acl is currently unpopulated.
	{6243, "pg_parameter_acl", 83, 'r', 3, true, pgParameterAclAttrs()},
	// M0106-0010 step 3ca: pg_replication_origin is opened during
	// PG-standby boot once Step 3bz cleared the pg_range family. Without
	// a pg_class row, `RelationBuildDesc(6000) → ScanPgRelation(6000)`
	// returns NULL and every forked backend FATALs with
	// `could not open relation with OID 6000`. RelType=83 is safe because
	// pg_replication_origin is not formrdesc'd (no
	// `ReplicationOriginRelation_Rowtype_Id` constant in PG18 headers;
	// only pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels), so the Phase3
	// `relation->rd_att->tdtypeid == relp->reltype` assertion
	// (relcache.c:4293) does not fire. Schema per
	// `postgres/src/include/catalog/pg_replication_origin.h` (PG18,
	// 2 cols: roident/roname). The empty 8 KiB heap at `global/6000` is
	// already produced by `bootstrapSharedCatalogPlaceholders`.
	{6000, "pg_replication_origin", 83, 'r', 2, true, pgReplicationOriginAttrs()},
	// M0106-0010 step 3ch: pg_tablespace is opened during PG-standby boot
	// once Step 3cg cleared the pg_subscription_rel family. Without a
	// pg_class row, `RelationBuildDesc(1213) → ScanPgRelation(1213)`
	// returns NULL and every forked backend FATALs with
	// `could not open relation with OID 1213`. RelType=83 is safe because
	// pg_tablespace is not formrdesc'd (no `TableSpaceRelation_Rowtype_Id`
	// constant in PG18 headers; only pg_database/pg_authid/pg_auth_members/
	// pg_shseclabel/pg_subscription are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`), so the
	// Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion
	// (relcache.c:4293) does not fire. Schema per
	// `postgres/src/include/catalog/pg_tablespace.h` (PG18, 5 cols:
	// oid/spcname/spcowner/spcacl/spcoptions). The empty 8 KiB heap at
	// `global/1213` is already produced by `bootstrapSharedCatalogPlaceholders`.
	{1213, "pg_tablespace", 83, 'r', 5, true, pgTablespaceAttrs()},
	// M0106-0010 step 3cu: pg_db_role_setting is opened by
	// `process_settings(MyDatabaseId, GetSessionUserId())` during
	// InitPostgres right after `CheckMyDatabase` returns. Without a
	// pg_class row, `RelationBuildDesc(2964) → ScanPgRelation(2964)`
	// returns NULL and every forked backend FATALs with
	// `could not open relation with OID 2964`. RelType=83 is safe because
	// pg_db_role_setting is not formrdesc'd (no
	// `DbRoleSettingRelation_Rowtype_Id` constant in PG18 headers; only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`), so the
	// Phase3 `relation->rd_att->tdtypeid == relp->reltype` assertion
	// (relcache.c:4293) does not fire. Schema per
	// `postgres/src/include/catalog/pg_db_role_setting.h` (PG18, 3 cols:
	// setdatabase/setrole/setconfig). The empty 8 KiB heap at
	// `global/2964` is produced by `bootstrapSharedCatalogPlaceholders`.
	{2964, "pg_db_role_setting", 83, 'r', 3, true, pgDbRoleSettingAttrs()},
}, []idxSpec{
	// M0106-0010 Step 3dh: pg_database_datname_index (OID 2671) is a
	// 1-column UNIQUE btree on pg_database.datname (name_ops) — same
	// shape as pg_authid_rolname_index. The default oid-stamped
	// descriptor would make PG's _bt_compare→index_getattr load the
	// first 4 inline NameData bytes of the leaf IndexTuple as a by-val
	// Datum and pass them to btnamecmp as a pointer (latent SEGV — the
	// exact bug fixed for OID 2676 in Step 3dg). The name-typed Attrs
	// override below pre-empts that crash for the next backend lookup
	// (InitPostgres → get_db_info() → SearchSysCache1(DATABASENAME, ...)).
	{OID: 2671, Name: "pg_database_datname_index", Attrs: []nailedAttr{
		{Name: "datname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
	}},
	{OID: 2672, Name: "pg_database_oid_index"},
	// M0106-0010 Step 3dg: pg_authid_rolname_index (OID 2676) is a
	// 1-column UNIQUE btree on pg_authid.rolname (name_ops). The
	// default oid-stamped descriptor (attlen=4, attbyval=1) was wrong:
	// PG's _bt_compare→index_getattr loaded the first 4 inline
	// NameData bytes of the leaf IndexTuple as a by-val Datum and
	// passed those bytes to btnamecmp as a pointer. For the OS-user
	// rolname "ryo" the 4 bytes are 0x72 0x79 0x6f 0x00 → Datum
	// 0x006f7972, which __strncmp_avx2 dereferenced → SIGSEGV
	// (si_addr=0x6f7972, RDI=0x6f7972 captured via the LD_PRELOAD
	// shim in Step 3df). The name-typed override below makes PG
	// read the key as a fixed-length 64-byte NameData pointer, which
	// is what btnamecmp expects.
	{OID: 2676, Name: "pg_authid_rolname_index", Attrs: []nailedAttr{
		{Name: "rolname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
	}},
	{OID: 2677, Name: "pg_authid_oid_index"},
	// M0106-0010 Step 3z: pg_auth_members_role_member_index. PG18
	// `postgres/src/include/catalog/pg_auth_members.h:49` declares
	// `AuthMemRoleMemIndexId = 2694` as a 3-column composite
	// btree(roleid, member, grantor). Without this entry
	// RelationIdGetRelation(2694) FATALs because no pg_class row
	// gets seeded; the sibling 2695 follows the same pattern.
	{OID: 2694, Name: "pg_auth_members_role_member_index"},
	{OID: 2695, Name: "pg_auth_members_member_role_index"},
	// M0106-0010 batched-13: pg_auth_members_oid_index (OID 6303).
	// PG18 `postgres/src/include/catalog/pg_auth_members.h:48` declares
	// `AuthMemOidIndexId = 6303` as UNIQUE PRIMARY btree(oid oid_ops).
	// PG's load_critical_index pass opens every declared index of a nailed
	// rel; without this entry RelationIdGetRelation(6303) FATALs because
	// no pg_class row gets seeded. Companion to OID 6302 (grantor, non-unique).
	{OID: 6303, Name: "pg_auth_members_oid_index"},
	// M0106-0010 batched-13: pg_auth_members_grantor_index (OID 6302).
	// PG18 `postgres/src/include/catalog/pg_auth_members.h:51` declares
	// `AuthMemGrantorIndexId = 6302` as btree(grantor oid_ops), non-unique.
	// Companion to OID 6303 (oid PKEY) over the pg_auth_members heap OID 1261.
	{OID: 6302, Name: "pg_auth_members_grantor_index"},
	{OID: 3593, Name: "pg_shseclabel_object_index"},
	// M0106-0010 Step 3bq: pg_parameter_acl_parname_index (OID 6246).
	// PG18 `postgres/src/include/catalog/pg_parameter_acl.h:53` declares
	// `ParameterAclParnameIndexId = 6246` as UNIQUE btree(parname text_ops).
	// Without this entry RelationIdGetRelation(6246) FATALs because no
	// pg_class row gets seeded for the index. Companion to OID 6247
	// (pg_parameter_acl_oid_index, UNIQUE PRIMARY) seeded in Step 3br below.
	{OID: 6246, Name: "pg_parameter_acl_parname_index"},
	// M0106-0010 Step 3br: pg_parameter_acl_oid_index (OID 6247).
	// PG18 `postgres/src/include/catalog/pg_parameter_acl.h:54` declares
	// `ParameterAclOidIndexId = 6247` as UNIQUE PRIMARY btree(oid oid_ops),
	// backing MAKE_SYSCACHE(PARAMETERACLOID, …). Without this entry
	// RelationIdGetRelation(6247) FATALs even though the Form_pg_index row
	// exists, because no pg_class row gets seeded. Sibling to OID 6246
	// (parname text_ops UNIQUE non-PKEY) from Step 3bq.
	{OID: 6247, Name: "pg_parameter_acl_oid_index"},
	// M0106-0010 Step 3ca: pg_replication_origin_roiident_index (OID 6001).
	// PG18 `postgres/src/include/catalog/pg_replication_origin.h:57` declares
	// `ReplicationOriginIdentIndex = 6001` as UNIQUE PRIMARY btree(roident
	// oid_ops), backing MAKE_SYSCACHE(REPLORIGIDENT, …). Without this entry
	// RelationIdGetRelation(6001) FATALs because no pg_class row gets seeded
	// for the index. Sibling to OID 6002 (roname text_ops UNIQUE non-PKEY).
	{OID: 6001, Name: "pg_replication_origin_roiident_index"},
	// M0106-0010 Step 3ca: pg_replication_origin_roname_index (OID 6002).
	// PG18 `postgres/src/include/catalog/pg_replication_origin.h:58` declares
	// `ReplicationOriginNameIndex = 6002` as UNIQUE btree(roname text_ops),
	// backing MAKE_SYSCACHE(REPLORIGNAME, …). Companion to OID 6001
	// (pg_replication_origin_roiident_index, UNIQUE PRIMARY).
	{OID: 6002, Name: "pg_replication_origin_roname_index"},
	// M0106-0010 Step 3cf: pg_subscription_oid_index (OID 6114).
	// PG18 `postgres/src/include/catalog/pg_subscription.h:103` declares
	// `SubscriptionObjectIndexId = 6114` as UNIQUE PRIMARY btree(oid
	// oid_ops), backing MAKE_SYSCACHE(SUBSCRIPTIONOID, …). PG's
	// load_critical_index pass opens every declared index of a nailed
	// rel; without this entry RelationIdGetRelation(6114) FATALs because
	// no pg_class row gets seeded.
	{OID: 6114, Name: "pg_subscription_oid_index"},
	// M0106-0010 Step 3cf: pg_subscription_subname_index (OID 6115).
	// PG18 `postgres/src/include/catalog/pg_subscription.h:104` declares
	// `SubscriptionNameIndexId = 6115` as UNIQUE composite
	// btree(subdbid oid_ops, subname name_ops), backing
	// MAKE_SYSCACHE(SUBSCRIPTIONNAME, …). Surfaced by
	// `TestE2E_FailoverGoopgToPG/async` as the next FATAL after Step 3ce
	// seeded pg_statistic. Companion to OID 6114 (oid PKEY) over the
	// already-nailed pg_subscription heap OID 6100.
	{OID: 6115, Name: "pg_subscription_subname_index"},
	// M0106-0010 Step 3ch: pg_tablespace_oid_index (OID 2697).
	// PG18 `postgres/src/include/catalog/pg_tablespace.h:52` declares
	// `TablespaceOidIndexId = 2697` as UNIQUE PRIMARY btree(oid oid_ops),
	// backing MAKE_SYSCACHE(TABLESPACEOID, …). PG's load_critical_index
	// pass opens every declared index of a nailed rel; without this entry
	// RelationIdGetRelation(2697) FATALs because no pg_class row gets
	// seeded for the index. Sibling to OID 2698 (spcname name_ops UNIQUE
	// non-PKEY).
	{OID: 2697, Name: "pg_tablespace_oid_index"},
	// M0106-0010 Step 3ch: pg_tablespace_spcname_index (OID 2698).
	// PG18 `postgres/src/include/catalog/pg_tablespace.h:53` declares
	// `TablespaceNameIndexId = 2698` as UNIQUE btree(spcname name_ops)
	// (no MAKE_SYSCACHE — used directly by get_tablespace_oid()).
	// Companion to OID 2697 (oid PKEY) over the pg_tablespace heap OID 1213
	// nailed in this same step.
	{OID: 2698, Name: "pg_tablespace_spcname_index"},
	// M0106-0010 Step 3cu: pg_db_role_setting_databaseid_rol_index (OID 2965).
	// PG18 `postgres/src/include/catalog/pg_db_role_setting.h:51` declares
	// `DECLARE_UNIQUE_INDEX_PKEY(pg_db_role_setting_databaseid_rol_index,
	// 2965, DbRoleSettingDatidRolidIndexId, pg_db_role_setting,
	// btree(setdatabase oid_ops, setrole oid_ops))`. There is no
	// MAKE_SYSCACHE — `process_settings` looks up rows via direct
	// index scan, not syscache. PG's load_critical_index pass opens
	// every declared index of a nailed rel; without this entry
	// RelationIdGetRelation(2965) FATALs because no pg_class row gets
	// seeded.
	{OID: 2965, Name: "pg_db_role_setting_databaseid_rol_index"},
})

// nailedLocalRels lists all local nailed relations (heaps + indexes flattened).
var nailedLocalRels = flattenRels([]nailedRel{
	{1247, "pg_type", 71, 'r', 14, false, pgTypeAttrs()},
	{1249, "pg_attribute", 75, 'r', 24, false, pgAttributeAttrs()},
	{1259, "pg_class", 83, 'r', 34, false, pgClassAttrs()},
	{1255, "pg_proc", 81, 'r', 30, false, pgProcAttrs()},
	{2610, "pg_index", 75, 'r', 21, false, pgIndexAttrs()},
	{2616, "pg_opclass", 83, 'r', 9, false, pgOpclassAttrs()},
	{2603, "pg_amproc", 83, 'r', 6, false, pgAmprocAttrs()},
	{2618, "pg_rewrite", 83, 'r', 8, false, pgRewriteAttrs()},
	{2620, "pg_trigger", 83, 'r', 8, false, pgTriggerAttrs()},
	{2615, "pg_namespace", 83, 'r', 5, false, pgNamespaceAttrs()},
	{2604, "pg_attrdef", 83, 'r', 4, false, pgAttrdefAttrs()},
	{2606, "pg_constraint", 83, 'r', 11, false, pgConstraintAttrs()},
	{2601, "pg_am", 83, 'r', 4, false, pgAmAttrs()},
	{2617, "pg_operator", 83, 'r', 10, false, pgOperatorAttrs()},
	{3456, "pg_collation", 83, 'r', 8, false, pgCollationAttrs()},
	{2611, "pg_inherits", 83, 'r', 3, false, pgInheritsAttrs()},
	{2612, "pg_language", 83, 'r', 7, false, pgLanguageAttrs()},
	{2602, "pg_amop", 83, 'r', 9, false, pgAmopAttrs()},
	{2609, "pg_description", 83, 'r', 5, false, pgDescriptionAttrs()},
	{2608, "pg_depend", 83, 'r', 8, false, pgDependAttrs()},
	// M0106-0010 step 3w: pg_aggregate is opened during InitPostgres'
	// `find_inheritance_children → table_open(AggregateRelationId)` path.
	// Without a pg_class row, PG FATALs with
	// `could not open relation with OID 2600`. RelType=83 is safe because
	// pg_aggregate is not formrdesc'd (no
	// `AggregateRelation_Rowtype_Id` constant in PG18 headers).
	{2600, "pg_aggregate", 83, 'r', 22, false, pgAggregateAttrs()},
	// M0106-0010 step 3aa: pg_cast is opened during early backend
	// startup by `find_coercion_pathway` / `parse_coerce` and FATALs
	// with `could not open relation with OID 2605` if no pg_class row
	// is seeded. RelType=83 is safe because pg_cast is not formrdesc'd
	// (no `CastRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_cast.h` (PG18).
	{2605, "pg_cast", 83, 'r', 6, false, pgCastAttrs()},
	// M0106-0010 step 3ag: pg_conversion is opened during PG-standby
	// boot once Steps 3w/3aa/3ab/3ac/3ad/3ae/3af cleared every prior
	// catalog FATAL. Without a pg_class row, `RelationBuildDesc(2607)
	// → ScanPgRelation(2607)` returns NULL and the backend FATALs with
	// `could not open relation with OID 2607`. RelType=83 is safe
	// because pg_conversion is not formrdesc'd (no
	// `ConversionRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_conversion.h` (PG18).
	{2607, "pg_conversion", 83, 'r', 8, false, pgConversionAttrs()},
	// M0106-0010 step 3ak: pg_default_acl is opened during PG-standby
	// boot once Steps 3w/3aa/3ag/etc. cleared every prior catalog
	// FATAL. Without a pg_class row, `RelationBuildDesc(826) →
	// ScanPgRelation(826)` returns NULL and the backend FATALs with
	// `could not open relation with OID 826`. RelType=83 is safe
	// because pg_default_acl is not formrdesc'd (no
	// `DefaultAclRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_default_acl.h` (PG18).
	{826, "pg_default_acl", 83, 'r', 5, false, pgDefaultAclAttrs()},
	// M0106-0010 step 3an: pg_enum is opened during PG-standby boot once
	// Step 3am cleared the pg_default_acl family. Without a pg_class row,
	// `RelationBuildDesc(3501) → ScanPgRelation(3501)` returns NULL and the
	// backend FATALs with `could not open relation with OID 3501`. RelType=83
	// is safe because pg_enum is not formrdesc'd (no
	// `EnumRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_enum.h` (PG18).
	{3501, "pg_enum", 83, 'r', 4, false, pgEnumAttrs()},
	// M0106-0010 step 3ar: pg_event_trigger is opened during PG-standby boot
	// once Step 3aq cleared the pg_enum family of indexes. Without a pg_class
	// row, `RelationBuildDesc(3466) → ScanPgRelation(3466)` returns NULL and
	// the backend FATALs with `could not open relation with OID 3466`.
	// RelType=83 is safe because pg_event_trigger is not formrdesc'd (no
	// `EventTriggerRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_event_trigger.h` (PG18,
	// EventTriggerRelationId == 3466). 7 columns; evttags is in the
	// CATALOG_VARLEN block so it is nullable.
	{3466, "pg_event_trigger", 83, 'r', 7, false, pgEventTriggerAttrs()},
	// M0106-0010 step 3aw: pg_extension is opened during PG-standby boot
	// once Step 3at cleared the pg_event_trigger family. Without a pg_class
	// row, `RelationBuildDesc(3079) → ScanPgRelation(3079)` returns NULL and
	// the backend FATALs with `could not open relation with OID 3079`.
	// RelType=83 is safe because pg_extension is not formrdesc'd (no
	// `ExtensionRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_extension.h` (PG18,
	// ExtensionRelationId == 3079). 8 columns; the trailing three
	// CATALOG_VARLEN columns (extversion text BKI_FORCE_NOT_NULL,
	// extconfig oid[], extcondition text[]) are stored as plain text in
	// the nailedAttr schema same as pg_event_trigger's evttags
	// convention — the heap is empty at bootstrap time so the encoder
	// is never exercised.
	{3079, "pg_extension", 83, 'r', 8, false, pgExtensionAttrs()},
	// M0106-0010 step 3bb: pg_foreign_data_wrapper is opened during PG-standby
	// boot once Step 3ba cleared the multi-leaf btree HIKEY blocker. Without a
	// pg_class row, `RelationBuildDesc(2328) → ScanPgRelation(2328)` returns
	// NULL and the backend FATALs with `could not open relation with OID 2328`.
	// RelType=83 is safe because pg_foreign_data_wrapper is not formrdesc'd
	// (no `ForeignDataWrapperRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_foreign_data_wrapper.h`
	// (PG18, ForeignDataWrapperRelationId == 2328). 7 columns; fdwacl and
	// fdwoptions are in the CATALOG_VARLEN block (nullable).
	{2328, "pg_foreign_data_wrapper", 83, 'r', 7, false, pgForeignDataWrapperAttrs()},
	// M0106-0010 step 3be: pg_foreign_server is opened during PG-standby
	// boot once Step 3bd cleared the pg_foreign_data_wrapper index family.
	// Without a pg_class row, `RelationBuildDesc(1417) → ScanPgRelation(1417)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 1417`. RelType=83 is safe because pg_foreign_server is not
	// formrdesc'd (no `ForeignServerRelation_Rowtype_Id` constant in PG18
	// headers). Schema per `postgres/src/include/catalog/pg_foreign_server.h`
	// (PG18, ForeignServerRelationId == 1417). 8 columns; the trailing
	// four CATALOG_VARLEN columns (srvtype, srvversion, srvacl, srvoptions)
	// are nullable.
	{1417, "pg_foreign_server", 83, 'r', 8, false, pgForeignServerAttrs()},
	// M0106-0010 step 3bh: pg_foreign_table is opened during PG-standby
	// boot once Step 3bg cleared both pg_foreign_server index OIDs.
	// Without a pg_class row, `RelationBuildDesc(3118) → ScanPgRelation(3118)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 3118`. RelType=83 is safe because pg_foreign_table is not
	// formrdesc'd (no `ForeignTableRelation_Rowtype_Id` constant in PG18
	// headers). Schema per `postgres/src/include/catalog/pg_foreign_table.h`
	// (PG18, ForeignTableRelationId == 3118). 3 columns; pg_foreign_table
	// has no `oid` system column — `ftrelid` is the primary key. The
	// trailing CATALOG_VARLEN ftoptions column is nullable.
	{3118, "pg_foreign_table", 83, 'r', 3, false, pgForeignTableAttrs()},
	// M0106-0010 step 3bm: pg_opfamily is opened during PG-standby boot
	// once Step 3bl cleared `pg_operator_oprname_l_r_n_index`. Without a
	// pg_class row, `RelationBuildDesc(2753) → ScanPgRelation(2753)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 2753`. RelType=83 is safe because pg_opfamily is not
	// formrdesc'd (no `OperatorFamilyRelation_Rowtype_Id` constant in
	// PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_opfamily.h` (PG18,
	// OperatorFamilyRelationId == 2753). 5 columns, all NotNull.
	{2753, "pg_opfamily", 83, 'r', 5, false, pgOpfamilyAttrs()},
	// M0106-0010 step 3bs: pg_partitioned_table is opened during PG-standby
	// boot once Step 3br cleared `pg_parameter_acl_oid_index`. Without a
	// pg_class row, `RelationBuildDesc(3350) → ScanPgRelation(3350)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 3350`. RelType=83 is safe because pg_partitioned_table is
	// not formrdesc'd (no `PartitionedRelation_Rowtype_Id` constant in
	// PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_partitioned_table.h` (PG18,
	// PartitionedRelationId == 3350). 8 columns: 4 fixed-width NotNull
	// (partrelid, partstrat, partnatts, partdefid) + 3 CATALOG_VARLEN
	// NotNull (partattrs int2vector, partclass oidvector, partcollation
	// oidvector — all BKI_FORCE_NOT_NULL) + 1 CATALOG_VARLEN nullable
	// (partexprs pg_node_tree).
	{3350, "pg_partitioned_table", 83, 'r', 8, false, pgPartitionedTableAttrs()},
	// M0106-0010 step 3bu: pg_publication is opened during PG-standby boot
	// once Step 3bt cleared `pg_partitioned_table_partrelid_index`. Without
	// a pg_class row, `RelationBuildDesc(6104) → ScanPgRelation(6104)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 6104`. RelType=83 is safe because pg_publication is not
	// formrdesc'd (no `PublicationRelation_Rowtype_Id` constant in
	// PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_publication.h` (PG18,
	// PublicationRelationId == 6104). 10 columns, all fixed-width NOT NULL
	// (oid, pubname, pubowner, puballtables, pubinsert, pubupdate,
	// pubdelete, pubtruncate, pubviaroot, pubgencols).
	{6104, "pg_publication", 83, 'r', 10, false, pgPublicationAttrs()},
	// M0106-0010 step 3bx: pg_publication_namespace is opened during
	// PG-standby boot once Step 3bw cleared `pg_publication_oid_index`.
	// Without a pg_class row, `RelationBuildDesc(6237) →
	// ScanPgRelation(6237)` returns NULL and the backend FATALs with
	// `could not open relation with OID 6237`. RelType=83 is safe because
	// pg_publication_namespace is not formrdesc'd (no
	// `PublicationNamespaceRelation_Rowtype_Id` constant in PG18
	// headers). Schema per
	// `postgres/src/include/catalog/pg_publication_namespace.h` (PG18,
	// PublicationNamespaceRelationId == 6237). 3 columns, all fixed-width
	// NOT NULL (oid, pnpubid, pnnspid).
	{6237, "pg_publication_namespace", 83, 'r', 3, false, pgPublicationNamespaceAttrs()},
	// M0106-0010 step 3by: pg_publication_rel is opened during PG-standby
	// boot once Step 3bx cleared the pg_publication_namespace family.
	// Without a pg_class row, `RelationBuildDesc(6106) →
	// ScanPgRelation(6106)` returns NULL and the backend FATALs with
	// `could not open relation with OID 6106`. RelType=83 is safe because
	// pg_publication_rel is not formrdesc'd (no
	// `PublicationRelRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_publication_rel.h`
	// (PG18, PublicationRelRelationId == 6106). 5 columns total: 3
	// fixed-width NOT NULL (oid, prpubid, prrelid) + 2 CATALOG_VARLEN
	// nullable (prqual pg_node_tree, prattrs int2vector).
	{6106, "pg_publication_rel", 83, 'r', 5, false, pgPublicationRelAttrs()},
	// M0106-0010 step 3bz: pg_range is opened during PG-standby boot once
	// Step 3by cleared the pg_publication_rel family. Without a pg_class row,
	// `RelationBuildDesc(3541) → ScanPgRelation(3541)` returns NULL and the
	// backend FATALs with `could not open relation with OID 3541`. RelType=83
	// is safe because pg_range is not formrdesc'd (no
	// `RangeRelation_Rowtype_Id` constant in PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_range.h` (PG18, RangeRelationId == 3541).
	// 7 columns total, all fixed-width NOT NULL (rngtypid, rngsubtype,
	// rngmultitypid, rngcollation, rngsubopc, rngcanonical, rngsubdiff).
	// pg_range has no `oid` system column — attnums start at 1.
	{3541, "pg_range", 83, 'r', 7, false, pgRangeAttrs()},
	// M0106-0010 step 3cb: pg_sequence is opened during PG-standby boot once
	// Step 3ca cleared the pg_replication_origin family. Without a pg_class
	// row, `RelationBuildDesc(2224) → ScanPgRelation(2224)` returns NULL and
	// the backend FATALs with `could not open relation with OID 2224`.
	// RelType=83 is safe because pg_sequence is not formrdesc'd (no
	// `SequenceRelation_Rowtype_Id` constant in PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_sequence.h` (PG18,
	// SequenceRelationId == 2224). 8 columns total, all fixed-width NOT NULL
	// (seqrelid oid, seqtypid oid, seqstart/seqincrement/seqmax/seqmin/seqcache
	// int8, seqcycle bool). pg_sequence has no `oid` system column — attnums
	// start at 1 = seqrelid.
	{2224, "pg_sequence", 83, 'r', 8, false, pgSequenceAttrs()},
	// M0106-0010 step 3cc: pg_statistic_ext_data is opened during PG-standby
	// boot once Step 3cb cleared the pg_sequence family. Without a pg_class
	// row, `RelationBuildDesc(3429) → ScanPgRelation(3429)` returns NULL and
	// the backend FATALs with `could not open relation with OID 3429`.
	// RelType=83 is safe because pg_statistic_ext_data is not formrdesc'd
	// (no `StatisticExtDataRelation_Rowtype_Id` constant in PG18 headers).
	// Schema per `postgres/src/include/catalog/pg_statistic_ext_data.h`
	// (PG18, StatisticExtDataRelationId == 3429). 6 columns total: 2 fixed
	// NOT NULL (stxoid oid, stxdinherit bool) + 4 CATALOG_VARLEN nullable
	// (stxdndistinct pg_ndistinct, stxddependencies pg_dependencies,
	// stxdmcv pg_mcv_list, stxdexpr _pg_statistic). pg_statistic_ext_data
	// has no `oid` system column — attnums start at 1 = stxoid.
	{3429, "pg_statistic_ext_data", 83, 'r', 6, false, pgStatisticExtDataAttrs()},
	// M0106-0010 step 3cd: pg_statistic_ext is opened during PG-standby
	// boot once Step 3cc cleared the pg_statistic_ext_data family. Without
	// a pg_class row, `RelationBuildDesc(3381) → ScanPgRelation(3381)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 3381`. RelType=83 is safe because pg_statistic_ext is not
	// formrdesc'd (no `StatisticExtRelation_Rowtype_Id` constant in PG18
	// headers). Schema per `postgres/src/include/catalog/pg_statistic_ext.h`
	// (PG18, StatisticExtRelationId == 3381). 9 columns total, all 5
	// leading fixed-width NOT NULL (oid, stxrelid, stxname, stxnamespace,
	// stxowner) + 1 CATALOG_VARLEN NOT NULL int2vector (stxkeys
	// BKI_FORCE_NOT_NULL) + 1 fixed-width nullable (stxstattarget int2
	// BKI_FORCE_NULL) + 1 CATALOG_VARLEN NOT NULL _char (stxkind
	// BKI_FORCE_NOT_NULL) + 1 CATALOG_VARLEN nullable pg_node_tree
	// (stxexprs). pg_statistic_ext DOES have an `oid` system column —
	// attnum 1 is oid.
	{3381, "pg_statistic_ext", 83, 'r', 9, false, pgStatisticExtAttrs()},
	// M0106-0010 step 3ce: pg_statistic is opened during PG-standby boot
	// once Step 3cd cleared the pg_statistic_ext family. Without a pg_class
	// row, `RelationBuildDesc(2619) → ScanPgRelation(2619)` returns NULL
	// and the backend FATALs with `could not open relation with OID 2619`.
	// RelType=83 is safe because pg_statistic is not formrdesc'd (no
	// `StatisticRelation_Rowtype_Id` constant in PG18 headers). Schema per
	// `postgres/src/include/catalog/pg_statistic.h` (PG18,
	// StatisticRelationId == 2619). 31 columns total: 3 leading fixed
	// NOT NULL key columns (starelid oid, staattnum int2, stainherit bool)
	// + 3 fixed NOT NULL stats (stanullfrac float4, stawidth int4,
	// stadistinct float4) + 5×stakind int2 NOT NULL + 5×staop oid NOT NULL
	// (BKI_LOOKUP_OPT, zero when unused) + 5×stacoll oid NOT NULL
	// (BKI_LOOKUP_OPT) + 5×stanumbers _float4 NULLABLE + 5×stavalues
	// anyarray NULLABLE. pg_statistic has no `oid` system column —
	// attnums start at 1 = starelid.
	{2619, "pg_statistic", 83, 'r', 31, false, pgStatisticAttrs()},
	// M0106-0010 step 3cg: pg_subscription_rel is opened during PG-standby
	// boot once Step 3cf cleared the pg_subscription index pair. Without a
	// pg_class row, `RelationBuildDesc(6102) → ScanPgRelation(6102)` returns
	// NULL and the backend FATALs with `could not open relation with OID
	// 6102`. RelType=83 is safe because pg_subscription_rel is not
	// formrdesc'd (no `SubscriptionRelRelation_Rowtype_Id` constant in PG18
	// headers). Schema per `postgres/src/include/catalog/pg_subscription_rel.h`
	// (PG18, SubscriptionRelRelationId == 6102). 4 columns: 3 leading fixed
	// NOT NULL (srsubid oid, srrelid oid, srsubstate char) + 1 fixed-width
	// nullable pg_lsn (srsublsn, BKI_FORCE_NULL inside CATALOG_VARLEN).
	// pg_subscription_rel has no `oid` system column — attnums start at
	// 1 = srsubid.
	{6102, "pg_subscription_rel", 83, 'r', 4, false, pgSubscriptionRelAttrs()},
	// M0106-0010 step 3ci: pg_transform is opened during PG-standby boot
	// once Step 3ch cleared the pg_tablespace family. Without a pg_class
	// row, `RelationBuildDesc(3576) → ScanPgRelation(3576)` returns NULL
	// and the backend FATALs with `could not open relation with OID 3576`.
	// RelType=83 is safe because pg_transform is not formrdesc'd (no
	// `TransformRelation_Rowtype_Id` constant in PG18 headers — only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_transform.h` (PG18,
	// TransformRelationId == 3576). 5 columns total: oid (26) + trftype
	// (26, BKI_LOOKUP pg_type) + trflang (26, BKI_LOOKUP pg_language) +
	// trffromsql (regproc 24, BKI_LOOKUP_OPT pg_proc) + trftosql (regproc
	// 24, BKI_LOOKUP_OPT pg_proc). All fixed-width NOT NULL — BKI_LOOKUP_OPT
	// means the FK lookup is optional (stores 0 when no function), but
	// the column itself stays NOT NULL. pg_transform DOES have an `oid`
	// system column — attnum 1 = oid.
	{3576, "pg_transform", 83, 'r', 5, false, pgTransformAttrs()},
	// M0106-0010 step 3cj: pg_ts_config_map is opened during PG-standby
	// boot once Step 3ci cleared the pg_transform family. Without a
	// pg_class row, `RelationBuildDesc(3603) → ScanPgRelation(3603)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 3603`. RelType=83 is safe because pg_ts_config_map is not
	// formrdesc'd (no `TSConfigMapRelation_Rowtype_Id` constant in PG18
	// headers — only pg_database/pg_authid/pg_auth_members/pg_shseclabel/
	// pg_subscription are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_ts_config_map.h` (PG18,
	// TSConfigMapRelationId == 3603). 4 columns total, all fixed-width
	// NOT NULL: mapcfg (oid 26, BKI_LOOKUP pg_ts_config) + maptokentype
	// (int4 23) + mapseqno (int4 23) + mapdict (oid 26, BKI_LOOKUP
	// pg_ts_dict). pg_ts_config_map has no `oid` system column —
	// attnums start at 1 = mapcfg.
	{3603, "pg_ts_config_map", 83, 'r', 4, false, pgTsConfigMapAttrs()},
	// M0106-0010 step 3ck: pg_ts_config is opened during PG-standby boot
	// once Step 3cj cleared the pg_ts_config_map family. Without a
	// pg_class row, `RelationBuildDesc(3602) → ScanPgRelation(3602)`
	// returns NULL and the backend FATALs with `could not open relation
	// with OID 3602`. RelType=83 is safe because pg_ts_config is not
	// formrdesc'd (no `TSConfigRelation_Rowtype_Id` constant in PG18
	// headers — only pg_database/pg_authid/pg_auth_members/pg_shseclabel/
	// pg_subscription are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_ts_config.h` (PG18,
	// TSConfigRelationId == 3602). 5 columns total, all fixed-width NOT
	// NULL: oid (26) + cfgname (name 19, 64-byte NameData) + cfgnamespace
	// (oid 26, BKI_LOOKUP pg_namespace) + cfgowner (oid 26, BKI_LOOKUP
	// pg_authid) + cfgparser (oid 26, BKI_LOOKUP pg_ts_parser). pg_ts_config
	// DOES have an `oid` system column — attnum 1 = oid.
	{3602, "pg_ts_config", 83, 'r', 5, false, pgTsConfigAttrs()},
	// M0106-0010 step 3cm: pg_ts_dict is opened during PG-standby boot
	// once Step 3ck cleared the pg_ts_config family. Without a pg_class
	// row, `RelationBuildDesc(3600) → ScanPgRelation(3600)` returns NULL
	// and the backend FATALs with `could not open relation with OID
	// 3600`. RelType=83 is safe because pg_ts_dict is not formrdesc'd
	// (no `TSDictionaryRelation_Rowtype_Id` constant in PG18 headers —
	// only pg_database/pg_authid/pg_auth_members/pg_shseclabel/
	// pg_subscription are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_ts_dict.h` (PG18,
	// TSDictionaryRelationId == 3600). 6 columns total: oid (26) +
	// dictname (name 19, 64-byte NameData) + dictnamespace (oid 26,
	// BKI_LOOKUP pg_namespace) + dictowner (oid 26, BKI_LOOKUP pg_authid)
	// + dicttemplate (oid 26, BKI_LOOKUP pg_ts_template) are all
	// fixed-width NOT NULL; dictinitoption (text 25) is CATALOG_VARLEN
	// NULLABLE. pg_ts_dict DOES have an `oid` system column — attnums
	// start at 1 = oid.
	{3600, "pg_ts_dict", 83, 'r', 6, false, pgTsDictAttrs()},
	// M0106-0010 step 3cn: pg_ts_parser is opened during PG-standby boot
	// once Step 3cm cleared pg_ts_dict. Without a pg_class row,
	// `RelationBuildDesc(3601) → ScanPgRelation(3601)` returns NULL
	// and the backend FATALs with `could not open relation with OID
	// 3601`. RelType=83 is safe because pg_ts_parser is not formrdesc'd
	// (no `TSParserRelation_Rowtype_Id` constant in PG18 headers — only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_ts_parser.h` (PG18,
	// TSParserRelationId == 3601). 8 columns total, all fixed-width
	// NOT NULL: oid (26) + prsname (name 19, 64-byte NameData) +
	// prsnamespace (oid 26, BKI_LOOKUP pg_namespace) + prsstart / prstoken
	// / prsend / prsheadline / prslextype (regproc 24, all BKI_LOOKUP
	// pg_proc; prsheadline is BKI_LOOKUP_OPT — the target proc may be
	// InvalidOid but the column itself is NOT NULL with value 0 when
	// absent). pg_ts_parser DOES have an `oid` system column — attnum
	// 1 = oid.
	{3601, "pg_ts_parser", 83, 'r', 8, false, pgTsParserAttrs()},
	// M0106-0010 step 3co: pg_ts_template is opened during PG-standby boot
	// once Step 3cn cleared pg_ts_parser. Without a pg_class row,
	// `RelationBuildDesc(3764) → ScanPgRelation(3764)` returns NULL
	// and the backend FATALs with `could not open relation with OID
	// 3764`. RelType=83 is safe because pg_ts_template is not formrdesc'd
	// (no `TSTemplateRelation_Rowtype_Id` constant in PG18 headers — only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd shared rels at
	// `postgres/src/backend/utils/cache/relcache.c:4075-4083`). Schema per
	// `postgres/src/include/catalog/pg_ts_template.h` (PG18,
	// TSTemplateRelationId == 3764). 5 columns total, all fixed-width
	// NOT NULL: oid (26) + tmplname (name 19, 64-byte NameData) +
	// tmplnamespace (oid 26, BKI_LOOKUP pg_namespace) + tmplinit / tmpllexize
	// (regproc 24; tmplinit is BKI_LOOKUP_OPT — the target proc may be
	// InvalidOid but the column itself is NOT NULL with value 0 when
	// absent, mirroring the prsheadline pattern in pg_ts_parser).
	// pg_ts_template DOES have an `oid` system column — attnum 1 = oid.
	{3764, "pg_ts_template", 83, 'r', 5, false, pgTsTemplateAttrs()},
	// M0106-0010 step 3cp: pg_user_mapping is opened during PG-standby
	// boot once Step 3co cleared pg_ts_template. Without a pg_class row,
	// `RelationBuildDesc(1418) → ScanPgRelation(1418)` returns NULL and
	// the backend FATALs with `could not open relation with OID 1418`.
	// RelType=83 is safe — pg_user_mapping is not formrdesc'd (no
	// `UserMappingRelation_Rowtype_Id` constant in PG18 headers, only
	// pg_database/pg_authid/pg_auth_members/pg_shseclabel/pg_subscription
	// are formrdesc'd at relcache.c:4075-4083). Schema per
	// `postgres/src/include/catalog/pg_user_mapping.h` (PG18,
	// UserMappingRelationId == 1418): 4 columns total — oid (26),
	// umuser (26, BKI_LOOKUP_OPT pg_authid; value 0 means PUBLIC),
	// umserver (26, BKI_LOOKUP pg_foreign_server), and umoptions
	// (text[] 1009, CATALOG_VARLEN — nullable). pg_user_mapping has
	// an `oid` system column — attnum 1 = oid.
	{1418, "pg_user_mapping", 83, 'r', 4, false, pgUserMappingAttrs()},
	// M0106-0010 Step 3dl: pg_stat_wal_receiver — first relkind='v' (view)
	// entry seeded into the bootstrap pg_class heap. After Steps 3dj/3dk
	// landed pg_proc OID 3317 (`pg_stat_get_wal_receiver`) with its 15
	// OUT-arg metadata arrays, PG can resolve the view's column list via
	// `build_function_result_tupdesc_d()`. This row makes PG's
	// `RangeVarGetRelid("pg_catalog.pg_stat_wal_receiver")` return a
	// non-zero OID so the E2E test's
	// `SELECT status FROM pg_catalog.pg_stat_wal_receiver` probe no longer
	// errors with `42P01 relation "pg_stat_wal_receiver" does not exist`.
	//
	// OID: PINNED to upstream by M0131-S8a (2026-08-11). This used to read
	// "12100 is a goopg-private stable assignment … there is no
	// upstream-canonical OID to mirror; 12100 is chosen unilaterally" —
	// that premise was wrong in the way that matters. PG assigns
	// system_views.sql view OIDs dynamically at initdb time, but the
	// assignment is DETERMINISTIC for a fixed build (verified across two
	// independent throwaway initdb runs), and a dependent view's captured
	// `ev_action` embeds its base view's assigned OID — so a
	// goopg-private choice is not free, it is a rewrite obligation on
	// every future blob. goopg therefore adopts upstream's values;
	// pg_stat_wal_receiver is 12240, not 12100. Policy, argument and the
	// full pinned table: internal/initdb/system_view_oid_pins.go and
	// docs/design/0131-0008-system-view-oid-policy.md.
	//
	// RelType=2249 (RECORDOID) matches the underlying function's
	// prorettype (pg_proc.dat:5670) so any code path that consults
	// pg_class.reltype gets a valid composite-type pointer (PG always
	// has the anonymous RECORD type registered).
	//
	// RelNatts=15 matches the OUT-arg list. pgClassRow handles
	// RelKind='v' specially: relam=0, relfilenode=0, relhasrules=true
	// (PG fetches the rewrite rule from pg_rewrite when relhasrules is
	// true). The pg_rewrite _RETURN rows carrying ev_action landed with
	// Step 3dm (pg_rewrite_bootstrap.go); M0131-S6 flipped relhasrules
	// on, so a hosted PG actually runs RelationBuildRuleLock over them.
	{pgStatWalReceiverViewOID, "pg_stat_wal_receiver", 2249, 'v', 15, false, pgStatWalReceiverAttrs()},
	// M0106-0010 batched-28: seed pg_class + pg_attribute rows for the 5
	// remaining replication views so PG's RangeVarGetRelid returns a non-zero
	// OID. RelType=2249 (RECORDOID) matches the underlying SRF's prorettype.
	// pg_rewrite _RETURN rows are deferred to batched-29 (Step 3dm).
	{pgStatReplicationViewOID, "pg_stat_replication", 2249, 'v', 20, false, pgStatReplicationViewAttrs()},
	{pgStatRecoveryPrefetchViewOID, "pg_stat_recovery_prefetch", 2249, 'v', 10, false, pgStatRecoveryPrefetchViewAttrs()},
	{pgStatSubscriptionViewOID, "pg_stat_subscription", 2249, 'v', 11, false, pgStatSubscriptionViewAttrs()},
	{pgReplicationSlotsViewOID, "pg_replication_slots", 2249, 'v', 21, false, pgReplicationSlotsViewAttrs()},
	{pgStatReplicationSlotsViewOID, "pg_stat_replication_slots", 2249, 'v', 10, false, pgStatReplicationSlotsViewAttrs()},
}, []idxSpec{
	{OID: 2703, Name: "pg_type_oid_index"},
	{OID: 2704, Name: "pg_type_typname_nsp_index"},
	{OID: 2658, Name: "pg_attribute_relid_attnam_index"},
	{OID: 2659, Name: "pg_attribute_relid_attnum_index"},
	{OID: 2662, Name: "pg_class_oid_index"},
	{OID: 2663, Name: "pg_class_relname_nsp_index"},
	{OID: 2690, Name: "pg_proc_oid_index"},
	{OID: 2691, Name: "pg_proc_proname_args_nsp_index"},
	{OID: 2678, Name: "pg_index_indrelid_index"},
	{OID: 2679, Name: "pg_index_indexrelid_index"},
	{OID: 2687, Name: "pg_opclass_oid_index"},
	{OID: 2655, Name: "pg_amproc_oid_index"},
	{OID: 2693, Name: "pg_rewrite_rel_rulename_index"},
	{OID: 2701, Name: "pg_trigger_tgrelid_tgname_index"},
	{OID: 2667, Name: "pg_constraint_oid_index"},
	// M0106-0010 batched-48: pg_constraint_conrelid_contypid_conname_index
	// (OID 2665, ConstraintRelidTypidNameIndexId). PG18's relcache build
	// path for any user table that may have NOT NULL constraints calls
	// CheckNNConstraintFetch (relcache.c:4615) which opens pg_constraint
	// via this composite UNIQUE index. Without a bootstrapped pg_class /
	// pg_attribute / pg_index entry the standby FATALs with
	//   "could not open relation with OID 2665"
	// the first time a backend parses a user table with NOT NULL columns
	// (e.g. TestE2E_FailoverGoopgToPG/async's bench_log).
	// btree(conrelid oid_ops, contypid oid_ops, conname name_ops),
	// indnatts=3, UNIQUE not PKEY.
	{OID: 2665, Name: "pg_constraint_conrelid_contypid_conname_index"},
	// B2.1b: pg_constraint_contypid_index (OID 2666, ConstraintTypidIndexId).
	// PG's domain typcache (GetDomainConstraints, typcache.c) opens
	// pg_constraint via this index on the FIRST use of any domain type —
	// without a bootstrapped pg_class/pg_attribute/pg_index entry the
	// standby raises "could not open relation with OID 2666" on every
	// domain-typed cast (e.g. 42::public.b2prep_dom post-failover).
	// btree(contypid oid_ops), indnatts=1, non-unique.
	{OID: 2666, Name: "pg_constraint_contypid_index"},
	{OID: 2688, Name: "pg_operator_oid_index"},
	// M-NIGHTLY AI-20260810-011258-003: pg_attrdef's two declared indexes
	// (postgres/src/include/catalog/pg_attrdef.h:53-54)
	//   AttrDefaultIndexId    = 2656 = pg_attrdef_adrelid_adnum_index
	//     btree(adrelid oid_ops, adnum int2_ops)  UNIQUE
	//   AttrDefaultOidIndexId = 2657 = pg_attrdef_oid_index
	//     btree(oid oid_ops)                      UNIQUE PRIMARY KEY
	// PG's AttrDefaultFetch (utils/cache/relcache.c) opens pg_attrdef BY
	// AttrDefaultIndexId whenever it builds the relcache entry of a table
	// with atthasdef, so a PG 18.3 standby streaming from goopg raised
	// `could not open relation with OID 2656` on EVERY query against a
	// table with a column DEFAULT. Same shape as pg_attribute_relid_attnum
	// _index (2659) / pg_class_oid_index (2662); runtime entries are
	// inserted by writeAttrdefRow (internal/executor/sys_pg_attrdef.go).
	{OID: 2656, Name: "pg_attrdef_adrelid_adnum_index"},
	{OID: 2657, Name: "pg_attrdef_oid_index"},
	{OID: 2680, Name: "pg_inherits_relid_seqno_index"},
	// M0106-0010 batched-36 loop 5: pg_namespace_nspname_index (OID 2684)
	// is a 1-column UNIQUE btree on pg_namespace.nspname (name_ops) — same
	// shape as pg_authid_rolname_index (Step 3dg) and pg_database_datname_index
	// (Step 3dh). Without this override flattenRels falls back to the
	// oid-stamped descriptor in indexKeyAttrs (attlen=4, attbyval=true),
	// which is written verbatim into pg_attribute (attrelid=2684, attnum=1)
	// by bootstrapPgAttributeTuples. RelationCacheInitializePhase3 then
	// builds a TupleDesc whose first key descriptor says attlen=4 /
	// attbyval=true. _bt_compare → index_getattr → fetchatt reads the first
	// 4 inline NameData bytes of the leaf IndexTuple as a by-val Datum and
	// passes them as a pointer to btnamecmp → strncmp(arg1, …) → SIGSEGV
	// (RDI=0x00000000745f6770 = LE "pg_t" — the leading bytes of either
	// "pg_catalog" or "pg_toast" in the root-leaf scanned for `public`).
	// The crash fires during parser/analyzer `RangeVarGetRelidExtended` →
	// `LookupExplicitNamespace("public")` → `SearchCatCache(NAMESPACENAME)`
	// when a PG18 standby promoted from a goopg basebackup runs the first
	// SELECT after recovery. The name-typed override below makes
	// bootstrapPgAttributeTuples emit attlen=64 / attbyval=false (NAMEOID),
	// which matches PG's expected ScanKey shape for name_ops.
	{OID: 2684, Name: "pg_namespace_nspname_index", Attrs: []nailedAttr{
		{Name: "nspname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
	}},
	{OID: 2685, Name: "pg_namespace_oid_index"},
	{OID: 2654, Name: "pg_amop_opr_fam_index"},
	// M0106-0010 Step 3x: pg_aggregate_fnoid_index. PG18
	// `postgres/src/include/catalog/pg_aggregate.h:113` declares
	// `AggregateFnoidIndexId = 2650`. RelationInitIndexAccessInfo's
	// `relnatts == indnatts` check (relcache.c:1492) requires this
	// entry to flow through flattenRels → pgIndexNattsByOID so the
	// nailed pg_class row's relnatts matches the pg_index row's
	// indnatts == 1.
	{OID: 2650, Name: "pg_aggregate_fnoid_index"},
	// M0106-0010 Step 3y: pg_amop_fam_strat_index. PG18
	// `postgres/src/include/catalog/pg_amop.h:90` declares
	// `AccessMethodStrategyIndexId = 2653` as a 4-column composite
	// btree(amopfamily, amoplefttype, amoprighttype, amopstrategy).
	// Without this entry RelationIdGetRelation(2653) FATALs because
	// no pg_class row gets seeded.
	{OID: 2653, Name: "pg_amop_fam_strat_index"},
	// M0106-0010 Step 3ab: pg_cast_oid_index. PG18
	// `postgres/src/include/catalog/pg_cast.h:59` declares
	// `CastOidIndexId = 2660` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Without this entry RelationIdGetRelation(2660)
	// FATALs because no pg_class row gets seeded; flattenRels also
	// derives RelNatts=1 via pgIndexNattsByOID, satisfying
	// RelationInitIndexAccessInfo's relnatts/indnatts check.
	{OID: 2660, Name: "pg_cast_oid_index"},
	// M0106-0010 Step 3ac: pg_cast_source_target_index. PG18
	// `postgres/src/include/catalog/pg_cast.h:60` declares
	// `CastSourceTargetIndexId = 2661` as UNIQUE (not PRIMARY) on
	// btree(castsource oid_ops, casttarget oid_ops). Without this entry
	// RelationIdGetRelation(2661) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=2 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check.
	{OID: 2661, Name: "pg_cast_source_target_index"},
	// M0106-0010 Step 3ad: pg_opclass_am_name_nsp_index. PG18
	// `postgres/src/include/catalog/pg_opclass.h:85` declares
	// `OpclassAmNameNspIndexId = 2686` as UNIQUE (not PRIMARY) on
	// btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops).
	// Without this entry RelationIdGetRelation(2686) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 2686, Name: "pg_opclass_am_name_nsp_index"},
	// M0106-0010 Step 3ae: pg_collation_name_enc_nsp_index. PG18
	// `postgres/src/include/catalog/pg_collation.h:62` declares
	// `CollationNameEncNspIndexId = 3164` as UNIQUE (not PRIMARY) on
	// btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops).
	// Without this entry RelationIdGetRelation(3164) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 3164, Name: "pg_collation_name_enc_nsp_index"},
	// M0106-0010 Step 3af: pg_collation_oid_index. PG18
	// `postgres/src/include/catalog/pg_collation.h:63` declares
	// `CollationOidIndexId = 3085` as UNIQUE PRIMARY on btree(oid oid_ops).
	// Without this entry RelationIdGetRelation(3085) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=1 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 3085, Name: "pg_collation_oid_index"},
	// M0106-0010 Step 3ah: pg_conversion_default_index. PG18
	// `postgres/src/include/catalog/pg_conversion.h:63` declares
	// `ConversionDefaultIndexId = 2668` as UNIQUE (not PRIMARY) on
	// btree(connamespace oid_ops, conforencoding int4_ops,
	//        contoencoding int4_ops, oid oid_ops).
	// Without this entry RelationIdGetRelation(2668) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=4 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 2668, Name: "pg_conversion_default_index"},
	// M0106-0010 Step 3ai: pg_conversion_oid_index. PG18
	// `postgres/src/include/catalog/pg_conversion.h:65` declares
	// `ConversionOidIndexId = 2670` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Companion to 2668 (Step 3ah composite UNIQUE
	// non-PKEY) and 2669 (conname/nsp composite UNIQUE non-PKEY, deferred
	// to Step 3aj). Without this entry RelationIdGetRelation(2670) FATALs
	// because no pg_class row gets seeded; flattenRels derives RelNatts=1
	// via pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 2670, Name: "pg_conversion_oid_index"},
	// M0106-0010 Step 3aj: pg_conversion_name_nsp_index. PG18
	// `postgres/src/include/catalog/pg_conversion.h:64` declares
	// `ConversionNameNspIndexId = 2669` as UNIQUE (not PRIMARY) on
	// btree(conname name_ops, connamespace oid_ops). Companion to 2668
	// (Step 3ah composite UNIQUE non-PKEY) and 2670 (Step 3ai UNIQUE
	// PRIMARY). Without this entry RelationIdGetRelation(2669) FATALs
	// because no pg_class row gets seeded; flattenRels derives
	// RelNatts=2 via pgIndexNattsByOID, satisfying
	// RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 2669, Name: "pg_conversion_name_nsp_index"},
	// M0106-0010 Step 3al: pg_default_acl_role_nsp_obj_index. PG18
	// `postgres/src/include/catalog/pg_default_acl.h:54` declares
	// `DefaultAclRoleNspObjIndexId = 827` as UNIQUE (not PRIMARY) on
	// btree(defaclrole oid_ops, defaclnamespace oid_ops,
	//       defaclobjtype char_ops). Companion to 828
	// (pg_default_acl_oid_index, UNIQUE PRIMARY KEY, to be seeded by
	// Step 3am). Backs MAKE_SYSCACHE(DEFACLROLENSPOBJ, …, 8). Without
	// this entry RelationIdGetRelation(827) FATALs because no pg_class
	// row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 827, Name: "pg_default_acl_role_nsp_obj_index"},
	// M0106-0010 Step 3am: pg_default_acl_oid_index. PG18
	// `postgres/src/include/catalog/pg_default_acl.h:55` declares
	// `DefaultAclOidIndexId = 828` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Companion to 827
	// (pg_default_acl_role_nsp_obj_index, UNIQUE non-PKEY, Step 3al).
	// Without this entry RelationIdGetRelation(828) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=1 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 828, Name: "pg_default_acl_oid_index"},
	// M0106-0010 Step 3ao: pg_enum_oid_index. PG18
	// `postgres/src/include/catalog/pg_enum.h:47` declares
	// `EnumOidIndexId = 3502` as UNIQUE PRIMARY KEY on btree(oid oid_ops).
	// Heap OID 3501 (pg_enum, Step 3an nailed rel). Companion index 3534
	// (pg_enum_typid_sortorder_index, UNIQUE composite float4_ops) is
	// seeded by Step 3aq below. Backs MAKE_SYSCACHE(ENUMOID,
	// pg_enum_oid_index, 8). Without this entry RelationIdGetRelation(3502)
	// FATALs because no pg_class row gets seeded; flattenRels derives
	// RelNatts=1 via pgIndexNattsByOID, satisfying
	// RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 3502, Name: "pg_enum_oid_index"},
	// M0106-0010 Step 3ap: pg_enum_typid_label_index. PG18
	// `postgres/src/include/catalog/pg_enum.h:48` declares
	// `EnumTypIdLabelIndexId = 3503` as UNIQUE (non-PKEY) composite on
	// btree(enumtypid oid_ops, enumlabel name_ops). Heap OID 3501
	// (pg_enum, Step 3an nailed rel). Backs MAKE_SYSCACHE(ENUMTYPOIDNAME,
	// pg_enum_typid_label_index, 8). Without this entry
	// RelationIdGetRelation(3503) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=2 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 3503, Name: "pg_enum_typid_label_index"},
	// M0106-0010 Step 3aq: pg_enum_typid_sortorder_index. PG18
	// `postgres/src/include/catalog/pg_enum.h:48` declares
	// `EnumTypIdSortOrderIndexId = 3534` as UNIQUE (non-PKEY) composite on
	// btree(enumtypid oid_ops, enumsortorder float4_ops). Heap OID 3501
	// (pg_enum, Step 3an nailed rel). First nailed index keyed on
	// `float4_ops` btree opclass (OID 10012 from postgres.bki). Without
	// this entry RelationIdGetRelation(3534) FATALs because no pg_class
	// row gets seeded; flattenRels derives RelNatts=2 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 3534, Name: "pg_enum_typid_sortorder_index"},
	// M0106-0010 Step 3as: pg_event_trigger_evtname_index. PG18
	// `postgres/src/include/catalog/pg_event_trigger.h:54` declares
	// `EventTriggerNameIndexId = 3467` as UNIQUE (non-PKEY) on
	// btree(evtname name_ops). Heap OID 3466 (pg_event_trigger,
	// Step 3ar nailed rel). Backs MAKE_SYSCACHE(EVENTTRIGGERNAME,
	// pg_event_trigger_evtname_index, 8). Without this entry
	// RelationIdGetRelation(3467) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	// M0106-0010 batched-36 loop 6: name-typed Attrs override (parity
		// with 2684 in loop 5). Without this override flattenRels falls
		// back to indexKeyAttrs(1)'s oid-stamped (attlen=4, attbyval=true)
		// descriptor; PG's _bt_compare reads the first 4 inline NameData
		// bytes of the leaf IndexTuple as a by-val Datum and passes them
		// to btnamecmp as a pointer → strncmp → SIGSEGV. evtname is a
		// single-column name_ops key.
		{OID: 3467, Name: "pg_event_trigger_evtname_index", Attrs: []nailedAttr{
			{Name: "evtname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
		}},
	// M0106-0010 Step 3at: pg_event_trigger_oid_index. PG18
	// `postgres/src/include/catalog/pg_event_trigger.h:55` declares
	// `EventTriggerOidIndexId = 3468` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Heap OID 3466 (pg_event_trigger,
	// Step 3ar nailed rel). Backs MAKE_SYSCACHE(EVENTTRIGGEROID,
	// pg_event_trigger_oid_index, 8). Without this entry
	// RelationIdGetRelation(3468) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 3468, Name: "pg_event_trigger_oid_index"},
	// M0106-0010 Step 3ax: pg_extension_oid_index. PG18
	// `postgres/src/include/catalog/pg_extension.h:56` declares
	// `ExtensionOidIndexId = 3080` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Heap OID 3079 (pg_extension,
	// Step 3aw nailed rel). Backs MAKE_SYSCACHE(EXTENSIONOID,
	// pg_extension_oid_index, 2). Without this entry
	// RelationIdGetRelation(3080) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 3080, Name: "pg_extension_oid_index"},
	// M0106-0010 Step 3ay: pg_extension_name_index. PG18
	// `postgres/src/include/catalog/pg_extension.h:57` declares
	// `ExtensionNameIndexId = 3081` as UNIQUE (non-PKEY) on
	// btree(extname name_ops). Heap OID 3079 (pg_extension,
	// Step 3aw nailed rel). Backs MAKE_SYSCACHE(EXTENSIONNAME,
	// pg_extension_name_index, 2). Without this entry
	// RelationIdGetRelation(3081) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	// M0106-0010 batched-36 loop 6: name-typed Attrs override (parity
		// with 2684). extname is a single-column name_ops key; missing
		// override here would crash PG's _bt_compare → btnamecmp the
		// same way as pg_namespace_nspname_index.
		{OID: 3081, Name: "pg_extension_name_index", Attrs: []nailedAttr{
			{Name: "extname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
		}},
	// M0106-0010 Step 3bc: pg_foreign_data_wrapper_name_index. PG18
	// `postgres/src/include/catalog/pg_foreign_data_wrapper.h:56`
	// declares `ForeignDataWrapperNameIndexId = 548` as UNIQUE
	// (non-PKEY) on btree(fdwname name_ops). Heap OID 2328
	// (pg_foreign_data_wrapper, Step 3bb nailed rel). Backs
	// MAKE_SYSCACHE(FOREIGNDATAWRAPPERNAME,
	// pg_foreign_data_wrapper_name_index, 2). Without this entry
	// RelationIdGetRelation(548) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	// M0106-0010 batched-36 loop 6: name-typed Attrs override (parity
		// with 2684). fdwname is a single-column name_ops key.
		{OID: 548, Name: "pg_foreign_data_wrapper_name_index", Attrs: []nailedAttr{
			{Name: "fdwname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
		}},
	// M0106-0010 Step 3bd: pg_foreign_data_wrapper_oid_index. PG18
	// `postgres/src/include/catalog/pg_foreign_data_wrapper.h:55`
	// declares `ForeignDataWrapperOidIndexId = 112` as UNIQUE PRIMARY
	// KEY on btree(oid oid_ops). Heap OID 2328
	// (pg_foreign_data_wrapper, Step 3bb nailed rel). Backs
	// MAKE_SYSCACHE(FOREIGNDATAWRAPPEROID,
	// pg_foreign_data_wrapper_oid_index, 2). Without this entry
	// RelationIdGetRelation(112) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 112, Name: "pg_foreign_data_wrapper_oid_index"},
	// M0106-0010 Step 3bf: pg_foreign_server_name_index. PG18
	// `postgres/src/include/catalog/pg_foreign_server.h:55`
	// declares `ForeignServerNameIndexId = 549` as UNIQUE
	// (non-PKEY) on btree(srvname name_ops). Heap OID 1417
	// (pg_foreign_server, Step 3be nailed rel). Backs
	// MAKE_SYSCACHE(FOREIGNSERVERNAME,
	// pg_foreign_server_name_index, 2). Without this entry
	// RelationIdGetRelation(549) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	// M0106-0010 batched-36 loop 6: name-typed Attrs override (parity
		// with 2684). srvname is a single-column name_ops key.
		{OID: 549, Name: "pg_foreign_server_name_index", Attrs: []nailedAttr{
			{Name: "srvname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
		}},
	// M0106-0010 Step 3bg: pg_foreign_server_oid_index. PG18
	// `postgres/src/include/catalog/pg_foreign_server.h:58`
	// declares `ForeignServerOidIndexId = 113` as UNIQUE PRIMARY
	// KEY on btree(oid oid_ops). Heap OID 1417
	// (pg_foreign_server, Step 3be nailed rel). Backs
	// MAKE_SYSCACHE(FOREIGNSERVEROID,
	// pg_foreign_server_oid_index, 2). Without this entry
	// RelationIdGetRelation(113) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 113, Name: "pg_foreign_server_oid_index"},
	// M0106-0010 Step 3bi: pg_foreign_table_relid_index. PG18
	// `postgres/src/include/catalog/pg_foreign_table.h:47`
	// declares `ForeignTableRelidIndexId = 3119` as UNIQUE PRIMARY
	// KEY on btree(ftrelid oid_ops). Heap OID 3118 (pg_foreign_table,
	// Step 3bh nailed rel). Backs MAKE_SYSCACHE(FOREIGNTABLEREL,
	// pg_foreign_table_relid_index, 4). Without this entry
	// RelationIdGetRelation(3119) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 3119, Name: "pg_foreign_table_relid_index"},
	// M0106-0010 Step 3bj: pg_language_name_index. PG18
	// `postgres/src/include/catalog/pg_language.h:69`
	// declares `LanguageNameIndexId = 2681` as UNIQUE (NOT primary)
	// on btree(lanname name_ops). Heap OID 2612 (pg_language) is
	// already a nailed local rel above. Backs MAKE_SYSCACHE(LANGNAME,
	// pg_language_name_index, 4). Without this entry
	// RelationIdGetRelation(2681) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	// M0106-0010 batched-36 loop 6: name-typed Attrs override (parity
		// with 2684). lanname is a single-column name_ops key.
		{OID: 2681, Name: "pg_language_name_index", Attrs: []nailedAttr{
			{Name: "lanname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
		}},
	// M0106-0010 Step 3bk: pg_language_oid_index. PG18
	// `postgres/src/include/catalog/pg_language.h:70`
	// declares `LanguageOidIndexId = 2682` as UNIQUE PRIMARY KEY
	// on btree(oid oid_ops). Heap OID 2612 (pg_language) is already a
	// nailed local rel above. Backs MAKE_SYSCACHE(LANGOID,
	// pg_language_oid_index, 4). Without this entry
	// RelationIdGetRelation(2682) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=1 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 2682, Name: "pg_language_oid_index"},
	// M0106-0010 Step 3bl: pg_operator_oprname_l_r_n_index. PG18
	// `postgres/src/include/catalog/pg_operator.h:86`
	// declares `OperatorNameNspIndexId = 2689` as UNIQUE (NOT primary)
	// on btree(oprname name_ops, oprleft oid_ops, oprright oid_ops,
	// oprnamespace oid_ops). Heap OID 2617 (pg_operator) is already a
	// nailed local rel above. Backs MAKE_SYSCACHE(OPERNAMENSP,
	// pg_operator_oprname_l_r_n_index, 256). Without this entry
	// RelationIdGetRelation(2689) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=4 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check
	// (relcache.c:1492).
	{OID: 2689, Name: "pg_operator_oprname_l_r_n_index"},
	// M0106-0010 Step 3bn: pg_opfamily_am_name_nsp_index. PG18
	// `postgres/src/include/catalog/pg_opfamily.h:47`
	// declares `OpfamilyAmNameNspIndexId = 2754` as UNIQUE (NOT primary)
	// on btree(opfmethod oid_ops, opfname name_ops,
	// opfnamespace oid_ops). Heap OID 2753 (pg_opfamily) is already a
	// nailed local rel above (Step 3bm). Backs
	// MAKE_SYSCACHE(OPFAMILYAMNAMENSP, pg_opfamily_am_name_nsp_index, 8).
	// Without this entry RelationIdGetRelation(2754) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 2754, Name: "pg_opfamily_am_name_nsp_index"},
	// M0106-0010 Step 3bo: pg_opfamily_oid_index.
	// `postgres/src/include/catalog/pg_opfamily.h:54`
	// declares `OpfamilyOidIndexId = 2755` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Heap OID 2753 (pg_opfamily) is already a
	// nailed local rel above (Step 3bm). Backs
	// MAKE_SYSCACHE(OPFAMILYOID, pg_opfamily_oid_index, 8).
	// Without this entry RelationIdGetRelation(2755) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=1 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{OID: 2755, Name: "pg_opfamily_oid_index"},
		// M0106-0010 Step 3bt: pg_partitioned_table_partrelid_index.
		// `postgres/src/include/catalog/pg_partitioned_table.h:69`
		// declares `PartitionedRelidIndexId = 3351` as UNIQUE PRIMARY KEY
		// on btree(partrelid oid_ops). Heap OID 3350 (pg_partitioned_table)
		// is already a nailed local rel above (Step 3bs). Backs
		// MAKE_SYSCACHE(PARTRELID, pg_partitioned_table_partrelid_index, 32).
		// Without this entry RelationIdGetRelation(3351) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 3351, Name: "pg_partitioned_table_partrelid_index"},
		// M0106-0010 Step 3bv: pg_publication_pubname_index.
		// `postgres/src/include/catalog/pg_publication.h:73`
		// declares `PublicationNameIndexId = 6111` as UNIQUE (NOT primary)
		// on btree(pubname name_ops). Heap OID 6104 (pg_publication) is
		// already a nailed local rel above (Step 3bu). Backs
		// MAKE_SYSCACHE(PUBLICATIONNAME, pg_publication_pubname_index, 8).
		// Without this entry RelationIdGetRelation(6111) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6111, Name: "pg_publication_pubname_index"},
		// M0106-0010 Step 3bw: pg_publication_oid_index.
		// `postgres/src/include/catalog/pg_publication.h:72`
		// declares `PublicationObjectIndexId = 6110` as UNIQUE PRIMARY KEY
		// on btree(oid oid_ops). Heap OID 6104 (pg_publication) is already
		// a nailed local rel above (Step 3bu). Backs
		// MAKE_SYSCACHE(PUBLICATIONOID, pg_publication_oid_index, 8).
		// Without this entry RelationIdGetRelation(6110) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6110, Name: "pg_publication_oid_index"},
		// M0106-0010 Step 3bx: pg_publication_namespace_oid_index.
		// `postgres/src/include/catalog/pg_publication_namespace.h:44`
		// declares `PublicationNamespaceObjectIndexId = 6238` as UNIQUE
		// PRIMARY KEY on btree(oid oid_ops). Heap OID 6237
		// (pg_publication_namespace) is already a nailed local rel above
		// (Step 3bx). Backs
		// MAKE_SYSCACHE(PUBLICATIONNAMESPACE, pg_publication_namespace_oid_index, 64).
		// Without this entry RelationIdGetRelation(6238) FATALs because
		// no pg_class row gets seeded; flattenRels derives RelNatts=1
		// via pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6238, Name: "pg_publication_namespace_oid_index"},
		// M0106-0010 Step 3bx: pg_publication_namespace_pnnspid_pnpubid_index.
		// `postgres/src/include/catalog/pg_publication_namespace.h:45`
		// declares `PublicationNamespacePnnspidPnpubidIndexId = 6239`
		// as UNIQUE (NOT primary) on btree(pnnspid oid_ops, pnpubid
		// oid_ops). Heap OID 6237 (pg_publication_namespace) is already
		// a nailed local rel above (Step 3bx). Backs
		// MAKE_SYSCACHE(PUBLICATIONNAMESPACEMAP,
		// pg_publication_namespace_pnnspid_pnpubid_index, 64). Without
		// this entry RelationIdGetRelation(6239) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=2 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6239, Name: "pg_publication_namespace_pnnspid_pnpubid_index"},
		// M0106-0010 Step 3by: pg_publication_rel_oid_index.
		// `postgres/src/include/catalog/pg_publication_rel.h:50`
		// declares `PublicationRelObjectIndexId = 6112` as UNIQUE
		// PRIMARY KEY on btree(oid oid_ops). Heap OID 6106
		// (pg_publication_rel) is already a nailed local rel above
		// (Step 3by). Backs MAKE_SYSCACHE(PUBLICATIONREL,
		// pg_publication_rel_oid_index, 64). Without this entry
		// RelationIdGetRelation(6112) FATALs because no pg_class row
		// gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6112, Name: "pg_publication_rel_oid_index"},
		// M0106-0010 Step 3by: pg_publication_rel_prrelid_prpubid_index.
		// `postgres/src/include/catalog/pg_publication_rel.h:51`
		// declares `PublicationRelPrrelidPrpubidIndexId = 6113`
		// as UNIQUE (NOT primary) on btree(prrelid oid_ops, prpubid
		// oid_ops). Heap OID 6106 (pg_publication_rel) is already a
		// nailed local rel above (Step 3by). Backs
		// MAKE_SYSCACHE(PUBLICATIONRELMAP,
		// pg_publication_rel_prrelid_prpubid_index, 64). Without this
		// entry RelationIdGetRelation(6113) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=2
		// via pgIndexNattsByOID, satisfying
		// RelationInitIndexAccessInfo's relnatts/indnatts check.
		{OID: 6113, Name: "pg_publication_rel_prrelid_prpubid_index"},
		// M0106-0010 Step 3by: pg_publication_rel_prpubid_index.
		// `postgres/src/include/catalog/pg_publication_rel.h:52`
		// declares `PublicationRelPrpubidIndexId = 6116` via
		// `DECLARE_INDEX` (non-UNIQUE) on btree(prpubid oid_ops).
		// Heap OID 6106 (pg_publication_rel) is already a nailed
		// local rel above (Step 3by). Used by
		// GetPublicationRelations() to enumerate relations for a
		// given publication. Without this entry
		// RelationIdGetRelation(6116) FATALs because no pg_class row
		// gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 6116, Name: "pg_publication_rel_prpubid_index"},
		// M0106-0010 Step 3bz: pg_range_rngtypid_index. PG18
		// `postgres/src/include/catalog/pg_range.h:60` declares
		// `RangeTypidIndexId = 3542` as UNIQUE PRIMARY KEY on
		// btree(rngtypid oid_ops). Heap OID 3541 (pg_range) is the
		// nailed local rel above (Step 3bz). Backs
		// MAKE_SYSCACHE(RANGETYPE, pg_range_rngtypid_index, 4). Without
		// this entry RelationIdGetRelation(3542) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 3542, Name: "pg_range_rngtypid_index"},
		// M0106-0010 Step 3bz: pg_range_rngmultitypid_index. PG18
		// `postgres/src/include/catalog/pg_range.h:61` declares
		// `RangeMultirangeTypidIndexId = 2228` as UNIQUE (NOT primary)
		// on btree(rngmultitypid oid_ops). Heap OID 3541 (pg_range) is
		// the nailed local rel above (Step 3bz). Backs
		// MAKE_SYSCACHE(RANGEMULTIRANGE, pg_range_rngmultitypid_index, 4).
		// Without this entry RelationIdGetRelation(2228) FATALs.
		{OID: 2228, Name: "pg_range_rngmultitypid_index"},
		// M0106-0010 Step 3cb: pg_sequence_seqrelid_index. PG18
		// `postgres/src/include/catalog/pg_sequence.h:42` declares
		// `SequenceRelidIndexId = 5002` as UNIQUE PRIMARY KEY on
		// btree(seqrelid oid_ops). Heap OID 2224 (pg_sequence) is the
		// nailed local rel above (Step 3cb). Backs
		// MAKE_SYSCACHE(SEQRELID, pg_sequence_seqrelid_index, 32). Without
		// this entry RelationIdGetRelation(5002) FATALs because no
		// pg_class row gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 5002, Name: "pg_sequence_seqrelid_index"},
		// M0106-0010 Step 3cc: pg_statistic_ext_data_stxoid_inh_index. PG18
		// `postgres/src/include/catalog/pg_statistic_ext_data.h:57` declares
		// `StatisticExtDataStxoidInhIndexId = 3433` as UNIQUE PRIMARY KEY on
		// btree(stxoid oid_ops, stxdinherit bool_ops). Composite key. Heap
		// OID 3429 (pg_statistic_ext_data) is the nailed local rel above
		// (Step 3cc). Backs MAKE_SYSCACHE(STATEXTDATASTXOID,
		// pg_statistic_ext_data_stxoid_inh_index, 4). Without this entry
		// RelationIdGetRelation(3433) FATALs; flattenRels derives RelNatts=2
		// via pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 3433, Name: "pg_statistic_ext_data_stxoid_inh_index"},
		// M0106-0010 Step 3cd: pg_statistic_ext_oid_index. PG18
		// `postgres/src/include/catalog/pg_statistic_ext.h:73` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_ext_oid_index, 3380,
		// StatisticExtOidIndexId, pg_statistic_ext, btree(oid oid_ops))`.
		// Heap OID 3381 (pg_statistic_ext) is the nailed local rel above
		// (Step 3cd). Backs MAKE_SYSCACHE(STATEXTOID,
		// pg_statistic_ext_oid_index, 4). Without this entry
		// RelationIdGetRelation(3380) FATALs because no pg_class row
		// gets seeded; flattenRels derives RelNatts=1 via
		// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
		// relnatts/indnatts check (relcache.c:1492).
		{OID: 3380, Name: "pg_statistic_ext_oid_index"},
		// M0106-0010 Step 3cd: pg_statistic_ext_name_index. PG18
		// `postgres/src/include/catalog/pg_statistic_ext.h:74` declares
		// `DECLARE_UNIQUE_INDEX(pg_statistic_ext_name_index, 3997,
		// StatisticExtNameIndexId, pg_statistic_ext, btree(stxname
		// name_ops, stxnamespace oid_ops))`. UNIQUE (NOT primary)
		// composite (2-column) key. Heap OID 3381 (pg_statistic_ext) is
		// the nailed local rel above (Step 3cd). Backs
		// MAKE_SYSCACHE(STATEXTNAMENSP, pg_statistic_ext_name_index, 4).
		// Without this entry RelationIdGetRelation(3997) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		// M0106-0010 batched-36 loop 6: composite (name, oid) Attrs override
		// (parity with 2684 in loop 5). stxname is the leading name_ops
		// key; without an override the leading column would be stamped by
		// indexKeyAttrs as oid (attlen=4, attbyval=true) and PG's
		// _bt_compare → btnamecmp would dereference the first 4 inline
		// NameData bytes of the leaf IndexTuple → SIGSEGV. Column 2
		// (stxnamespace) is oid_ops so the indexKeyAttrs default would
		// already be correct, but we spell it out for clarity.
		{OID: 3997, Name: "pg_statistic_ext_name_index", Attrs: []nailedAttr{
			{Name: "stxname", TypeOID: 19, Num: 1, Len: 64, NotNull: true},
			{Name: "stxnamespace", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		}},
		// M0106-0010 Step 3cd: pg_statistic_ext_relid_index. PG18
		// `postgres/src/include/catalog/pg_statistic_ext.h:75` declares
		// `DECLARE_INDEX(pg_statistic_ext_relid_index, 3379,
		// StatisticExtRelidIndexId, pg_statistic_ext, btree(stxrelid
		// oid_ops))`. NON-UNIQUE. Heap OID 3381 (pg_statistic_ext) is the
		// nailed local rel above (Step 3cd). Used by
		// RemoveStatisticsExtById / dependency cleanup paths to enumerate
		// extended-statistics objects defined on a given table. Without
		// this entry RelationIdGetRelation(3379) FATALs; flattenRels
		// derives RelNatts=1.
		{OID: 3379, Name: "pg_statistic_ext_relid_index"},
		// M0106-0010 Step 3ce: pg_statistic_relid_att_inh_index. PG18
		// `postgres/src/include/catalog/pg_statistic.h:139` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_statistic_relid_att_inh_index, 2696,
		// StatisticRelidAttnumInhIndexId, pg_statistic, btree(starelid
		// oid_ops, staattnum int2_ops, stainherit bool_ops))`. UNIQUE
		// PRIMARY 3-column composite key. Heap OID 2619 (pg_statistic) is
		// the nailed local rel above (Step 3ce). Backs
		// MAKE_SYSCACHE(STATRELATTINH, pg_statistic_relid_att_inh_index, 128).
		// Without this entry RelationIdGetRelation(2696) FATALs; flattenRels
		// derives RelNatts=3 via pgIndexNattsByOID.
		{OID: 2696, Name: "pg_statistic_relid_att_inh_index"},
		// M0106-0010 Step 3cg: pg_subscription_rel_srrelid_srsubid_index. PG18
		// `postgres/src/include/catalog/pg_subscription_rel.h:52` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_subscription_rel_srrelid_srsubid_index,
		// 6117, SubscriptionRelSrrelidSrsubidIndexId, pg_subscription_rel,
		// btree(srrelid oid_ops, srsubid oid_ops))`. UNIQUE PRIMARY 2-column
		// composite key. Heap OID 6102 (pg_subscription_rel) is the nailed
		// local rel above. Backs MAKE_SYSCACHE(SUBSCRIPTIONRELMAP,
		// pg_subscription_rel_srrelid_srsubid_index, 64). Without this entry
		// RelationIdGetRelation(6117) FATALs; flattenRels derives RelNatts=2
		// via pgIndexNattsByOID.
		{OID: 6117, Name: "pg_subscription_rel_srrelid_srsubid_index"},
		// M0106-0010 Step 3ci: pg_transform_oid_index. PG18
		// `postgres/src/include/catalog/pg_transform.h:43` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_transform_oid_index, 3574,
		// TransformOidIndexId, pg_transform, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3576 (pg_transform) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TRFOID,
		// pg_transform_oid_index, 16). Without this entry
		// RelationIdGetRelation(3574) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{OID: 3574, Name: "pg_transform_oid_index"},
		// M0106-0010 Step 3ci: pg_transform_type_lang_index. PG18
		// `postgres/src/include/catalog/pg_transform.h:44` declares
		// `DECLARE_UNIQUE_INDEX(pg_transform_type_lang_index, 3575,
		// TransformTypeLangIndexId, pg_transform, btree(trftype oid_ops,
		// trflang oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite key,
		// both oid_ops with no collation. Heap OID 3576 (pg_transform) is
		// the nailed local rel above. Backs MAKE_SYSCACHE(TRFTYPELANG,
		// pg_transform_type_lang_index, 16). Without this entry
		// RelationIdGetRelation(3575) FATALs; flattenRels derives
		// RelNatts=2 via pgIndexNattsByOID.
		{OID: 3575, Name: "pg_transform_type_lang_index"},
		// M0106-0010 Step 3cj: pg_ts_config_map_index. PG18
		// `postgres/src/include/catalog/pg_ts_config_map.h:48` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_map_index, 3609,
		// TSConfigMapIndexId, pg_ts_config_map, btree(mapcfg oid_ops,
		// maptokentype int4_ops, mapseqno int4_ops))`. UNIQUE PRIMARY
		// 3-column composite key. Heap OID 3603 (pg_ts_config_map) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSCONFIGMAP,
		// pg_ts_config_map_index, 2). Without this entry
		// RelationIdGetRelation(3609) FATALs; flattenRels derives
		// RelNatts=3 via pgIndexNattsByOID.
		{OID: 3609, Name: "pg_ts_config_map_index"},
		// M0106-0010 Step 3ck: pg_ts_config_cfgname_index. PG18
		// `postgres/src/include/catalog/pg_ts_config.h:50` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_config_cfgname_index, 3608,
		// TSConfigNameNspIndexId, pg_ts_config, btree(cfgname name_ops,
		// cfgnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3602 (pg_ts_config) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSCONFIGNAMENSP, pg_ts_config_cfgname_index, 2).
		// Without this entry RelationIdGetRelation(3608) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{OID: 3608, Name: "pg_ts_config_cfgname_index"},
		// M0106-0010 Step 3ck: pg_ts_config_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_config.h:51` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_oid_index, 3712,
		// TSConfigOidIndexId, pg_ts_config, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3602 (pg_ts_config) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSCONFIGOID,
		// pg_ts_config_oid_index, 2). Without this entry
		// RelationIdGetRelation(3712) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{OID: 3712, Name: "pg_ts_config_oid_index"},
		// M0106-0010 Step 3cm: pg_ts_dict_dictname_index. PG18
		// `postgres/src/include/catalog/pg_ts_dict.h:56` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_dict_dictname_index, 3604,
		// TSDictionaryNameNspIndexId, pg_ts_dict, btree(dictname name_ops,
		// dictnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3600 (pg_ts_dict) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSDICTNAMENSP, pg_ts_dict_dictname_index, 2).
		// Without this entry RelationIdGetRelation(3604) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{OID: 3604, Name: "pg_ts_dict_dictname_index"},
		// M0106-0010 Step 3cm: pg_ts_dict_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_dict.h:57` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_dict_oid_index, 3605,
		// TSDictionaryOidIndexId, pg_ts_dict, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3600 (pg_ts_dict) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSDICTOID,
		// pg_ts_dict_oid_index, 2). Without this entry
		// RelationIdGetRelation(3605) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{OID: 3605, Name: "pg_ts_dict_oid_index"},
		// M0106-0010 Step 3cn: pg_ts_parser_prsname_index. PG18
		// `postgres/src/include/catalog/pg_ts_parser.h:56` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_parser_prsname_index, 3606,
		// TSParserNameNspIndexId, pg_ts_parser, btree(prsname name_ops,
		// prsnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3601 (pg_ts_parser) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSPARSERNAMENSP, pg_ts_parser_prsname_index, 2).
		// Without this entry RelationIdGetRelation(3606) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{OID: 3606, Name: "pg_ts_parser_prsname_index"},
		// M0106-0010 Step 3cn: pg_ts_parser_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_parser.h:57` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_parser_oid_index, 3607,
		// TSParserOidIndexId, pg_ts_parser, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3601 (pg_ts_parser) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSPARSEROID,
		// pg_ts_parser_oid_index, 2). Without this entry
		// RelationIdGetRelation(3607) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{OID: 3607, Name: "pg_ts_parser_oid_index"},
		// M0106-0010 Step 3co: pg_ts_template_tmplname_index. PG18
		// `postgres/src/include/catalog/pg_ts_template.h:48` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_template_tmplname_index, 3766,
		// TSTemplateNameNspIndexId, pg_ts_template, btree(tmplname name_ops,
		// tmplnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3764 (pg_ts_template) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSTEMPLATENAMENSP, pg_ts_template_tmplname_index, 2).
		// Without this entry RelationIdGetRelation(3766) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{OID: 3766, Name: "pg_ts_template_tmplname_index"},
		// M0106-0010 Step 3co: pg_ts_template_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_template.h:49` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_template_oid_index, 3767,
		// TSTemplateOidIndexId, pg_ts_template, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3764 (pg_ts_template) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSTEMPLATEOID,
		// pg_ts_template_oid_index, 2). Without this entry
		// RelationIdGetRelation(3767) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{OID: 3767, Name: "pg_ts_template_oid_index"},
		// M0106-0010 Step 3cp: pg_user_mapping_oid_index. PG18
		// `postgres/src/include/catalog/pg_user_mapping.h:52` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_user_mapping_oid_index, 174,
		// UserMappingOidIndexId, pg_user_mapping, btree(oid oid_ops))`.
		// UNIQUE PRIMARY single-column key on attnum 1 (oid). Heap OID
		// 1418 (pg_user_mapping) is the nailed local rel above. Backs
		// MAKE_SYSCACHE(USERMAPPINGOID, pg_user_mapping_oid_index, 2).
		// Without this entry RelationIdGetRelation(174) FATALs; flattenRels
		// derives RelNatts=1 via pgIndexNattsByOID.
		{OID: 174, Name: "pg_user_mapping_oid_index"},
		// M0106-0010 Step 3cp: pg_user_mapping_user_server_index. PG18
		// `postgres/src/include/catalog/pg_user_mapping.h:53` declares
		// `DECLARE_UNIQUE_INDEX(pg_user_mapping_user_server_index, 175,
		// UserMappingUserServerIndexId, pg_user_mapping,
		// btree(umuser oid_ops, umserver oid_ops))`. UNIQUE (NOT PRIMARY)
		// 2-column composite on (umuser=2, umserver=3); both oid_ops with
		// no collation. Backs MAKE_SYSCACHE(USERMAPPINGUSERSERVER,
		// pg_user_mapping_user_server_index, 2). flattenRels derives
		// RelNatts=2 via pgIndexNattsByOID.
		{OID: 175, Name: "pg_user_mapping_user_server_index"},
	})

func indexNailed(oid uint32, name string, natts int16, attrs []nailedAttr) nailedRel {
	if attrs == nil {
		attrs = indexKeyAttrs(natts)
	}
	return nailedRel{
		OID: oid, RelName: name, RelKind: 'i',
		RelNatts: natts, RelType: 0,
		Attrs: attrs,
	}
}

type idxSpec struct {
		OID  uint32
		Name string
		// Attrs, when non-nil, overrides the default oid-stamped descriptors
		// from indexKeyAttrs. Required for any index whose first key is a
		// non-oid type (e.g. name_ops, text_ops). PG's _bt_compare reads
		// values via index_getattr using these attlen/attbyval/attalign
		// fields; if attlen=4/attbyval=1 is left on a name-typed key, PG
		// loads 4 inline NameData bytes ("ryo\0") as a by-val Datum and
		// passes it to btnamecmp as a pointer → SIGSEGV in __strncmp_avx2.
		// Step 3dg: discovered via SEGV shim attribution
		// (si_addr=0x6f7972 == RDI, the "ryo\0" inline bytes).
		Attrs []nailedAttr
	}

func flattenRels(heaps []nailedRel, idxs []idxSpec) []nailedRel {
	var out []nailedRel
	out = append(out, heaps...)
	natts := pgIndexNattsByOID()
	// M0106-0010 Step 3cr: indexes inherit IsShared from their parent
	// heap. All heaps in a single flattenRels call share the same
	// IsShared (nailedSharedRels is all-shared, nailedLocalRels is
	// all-local), so propagating from heaps[0] is unambiguous.
	// Without this, the per-database pg_class entry for a shared index
	// such as 2672 (pg_database_oid_index) had relisshared=false and
	// reltablespace=0, so PG's RelationInitPhysicalAddr resolved the
	// file path to `base/<MyDatabaseId>/2672` instead of `global/2672`
	// and standby user backends FATAL'd with
	//   could not open file "base/5/2672": No such file or directory
	// after Step 3cq let them past InitPostgres.
	isShared := len(heaps) > 0 && heaps[0].IsShared

	// M0106-0010 batched-36 loop 7: build a heap-attribute lookup table
	// so an idxSpec without an explicit Attrs override gets its key-
	// column descriptors *derived from the heap rel's pg_attribute
	// shape* rather than the all-OID `indexKeyAttrs(natts)` fallback.
	// Without this, every composite index whose indkey references a
	// `name`/`text`/`bytea` column (e.g. 2663 pg_class_relname_nsp_index,
	// 2658 pg_attribute_relid_attnam_index, 2691 pg_proc_proname_args_
	// nsp_index, 2686 pg_opclass_am_name_nsp_index, 3164 pg_collation_
	// name_enc_nsp_index, …) writes attlen=4 / attbyval=true into
	// pg_attribute for the FIRST non-oid key column. PG's
	// RelationCacheInitializePhase3 then builds a TupleDesc with the
	// wrong shape; _bt_compare → index_getattr → fetchatt returns the
	// first 4 inline NameData bytes of the indexed value as a by-val
	// Datum and passes that "pointer" to btnamecmp/byteacmp/textcmp →
	// SIGSEGV on the parse-analyze path of any user-table query (the
	// failure mode that has blocked TestE2E_FailoverGoopgToPG/async
	// across loops 4–6 of this batched task). The explicit overrides on
	// e.g. OID 2684 (loop 5) and the six name-typed UNIQUE entries from
	// loop 6 take precedence; this auto-derivation supplies the same
	// fix for every remaining idxSpec without requiring 18+ hand-edits.
	heapAttrByOID := make(map[uint32]map[int16]nailedAttr, len(heaps))
	for _, h := range heaps {
		m := make(map[int16]nailedAttr, len(h.Attrs))
		for _, a := range h.Attrs {
			m[a.Num] = a
		}
		heapAttrByOID[h.OID] = m
	}
	indexSeedByOID := make(map[uint32]pgIndexEntry)
	for _, e := range pgIndexInitialEntries() {
		indexSeedByOID[e.IndexRelid] = e
	}

	for _, idx := range idxs {
		// Each index's natts MUST equal its pg_index.indnatts; PG's
		// RelationInitIndexAccessInfo asserts relnatts == indnatts and
		// FATALs with "relnatts disagrees with indnatts for index <oid>"
		// otherwise (postgres/src/backend/utils/cache/relcache.c:1492).
		n, ok := natts[idx.OID]
		if !ok {
			// Fallback for any index without a pg_index seed row. 1 is
			// the most common arity and matches the historical default
			// for OID-keyed unique indexes.
			n = 1
		}
		attrs := idx.Attrs
		if attrs == nil {
			if derived, ok := deriveIndexAttrsFromHeap(idx.OID, n, indexSeedByOID, heapAttrByOID); ok {
				attrs = derived
			}
		}
		ix := indexNailed(idx.OID, idx.Name, n, attrs)
		ix.IsShared = isShared
		out = append(out, ix)
	}
	return out
}

// deriveIndexAttrsFromHeap looks up the pg_index seed row for idxOID
// and resolves each indkey entry against the heap relation's Attrs map.
// Returns (attrs, true) iff every key column resolves (i.e. the index
// is a simple-column index — not expressional — on a heap that is
// itself nailed in the same flattenRels call). On any miss the caller
// is expected to fall back to indexKeyAttrs(natts), preserving prior
// behavior for indexes whose heap is not part of the nailed set.
func deriveIndexAttrsFromHeap(idxOID uint32, natts int16, seedByOID map[uint32]pgIndexEntry, heapAttrByOID map[uint32]map[int16]nailedAttr) ([]nailedAttr, bool) {
	seed, ok := seedByOID[idxOID]
	if !ok {
		return nil, false
	}
	if int16(len(seed.IndKey)) != natts {
		return nil, false
	}
	heapMap, ok := heapAttrByOID[seed.IndRelid]
	if !ok {
		return nil, false
	}
	out := make([]nailedAttr, 0, len(seed.IndKey))
	for i, k := range seed.IndKey {
		if k <= 0 {
			// Expressional index column (indkey=0): no heap attribute
			// to derive from. Caller falls back to indexKeyAttrs.
			return nil, false
		}
		ha, ok := heapMap[k]
		if !ok {
			return nil, false
		}
		out = append(out, nailedAttr{
			Name:    ha.Name,
			TypeOID: ha.TypeOID,
			Num:     int16(i + 1),
			Len:     ha.Len,
			NotNull: true,
		})
	}
	return out, true
}

func indexKeyAttrs(natts int16) []nailedAttr {
	attrs := make([]nailedAttr, natts)
	for i := int16(0); i < natts; i++ {
		attrs[i] = nailedAttr{
			Name:    "oid",
			TypeOID: 26, // OID type
			Num:     i + 1,
			Len:     4,  // OID = 4 bytes
			NotNull: true,
		}
	}
	return attrs
}


// === Relcache init file: index sub-record support (batched-30) ===

const (
	// heapTupleDataSize is sizeof(HeapTupleData) = HEAPTUPLESIZE on amd64.
	// uint32(4) + ItemPointerData(6) + Oid(4) + pad(2) + ptr(8) = 24.
	heapTupleDataSize = 24

	// pgIndexTupleHdrSize is SizeofHeapTupleHeader =
	// offsetof(HeapTupleHeaderData, t_bits) = 23 on all PG platforms.
	pgIndexTupleHdrSize = 23

	// pgIndexNumCols is the number of columns in pg_index (PG18).
	pgIndexNumCols = 21

	// pgIndexNullBitmapBytes is ceil(21/8) = 3.
	pgIndexNullBitmapBytes = 3

	// pgIndexTupleHoff is t_hoff for a pg_index tuple with 21 cols and 2 NULLs.
	// MAXALIGN(23 + 3) = 32 on 64-bit systems.
	pgIndexTupleHoff = 32

	// btreeAmsupport is BTNProcs = 6 for PG18's btree AM (nbtree.h:723).
	// Support procs 1-4 and 6 exist; slot 5 is unused but still counted.
	btreeAmsupport = 6
)

// criticalSharedHeapOIDs / criticalSharedIndexOIDs are the canonical
// PG18 nailed-rel sets: NUM_CRITICAL_SHARED_RELS=5 (relcache.c:4086) and
// NUM_CRITICAL_SHARED_INDEXES=6 (relcache.c:4226). Order matches the
// formrdesc + load_critical_index calls at relcache.c:4075-4226.
var (
	criticalSharedHeapOIDs  = []uint32{1262, 1260, 1261, 3592, 6100}
	criticalSharedIndexOIDs = []uint32{2671, 2672, 2676, 2677, 2695, 3593}

	// NUM_CRITICAL_LOCAL_RELS=4 (relcache.c:4143), NUM_CRITICAL_LOCAL_INDEXES=7 (:4194).
	criticalLocalHeapOIDs  = []uint32{1259, 1249, 1255, 1247}
	criticalLocalIndexOIDs = []uint32{2662, 2659, 2679, 2687, 2655, 2693, 2701}
)

// opcClassInfo holds the opfamily and input type for a pg_opclass row.
type opcClassInfo struct {
	family uint32
	intype uint32
}

// amprocCompKey identifies a (family, input-type) pair for amproc lookups.
type amprocCompKey struct {
	family uint32
	intype uint32
}

// buildOpcInfoByOID returns an OID→{family,intype} map built from
// pgOpclassInitialEntries.
func buildOpcInfoByOID() map[uint32]opcClassInfo {
	m := make(map[uint32]opcClassInfo)
	for _, e := range pgOpclassInitialEntries() {
		m[e.OID] = opcClassInfo{family: e.Family, intype: e.IntType}
	}
	return m
}

// buildAmprocCompByKey returns a {family,intype}→procOID map for btree
// support proc #1 (comparison function) from pgAmprocInitialEntries.
func buildAmprocCompByKey() map[amprocCompKey]uint32 {
	m := make(map[amprocCompKey]uint32)
	for _, e := range pgAmprocInitialEntries() {
		if e.Num == 1 {
			m[amprocCompKey{family: e.Family, intype: e.LeftType}] = e.Proc
		}
	}
	return m
}

// buildPgIndexEntryByRelid returns an indexRelid→pgIndexEntry map built
// from pgIndexInitialEntries.
func buildPgIndexEntryByRelid() map[uint32]pgIndexEntry {
	m := make(map[uint32]pgIndexEntry)
	for _, e := range pgIndexInitialEntries() {
		m[e.IndexRelid] = e
	}
	return m
}

// filterCriticalRels extracts exactly the canonical heap + index records
// from rels in the order required by PG's trailing-count check
// (relcache.c:6510-6547). Heaps are emitted before indexes.
func filterCriticalRels(rels []nailedRel, heapOIDs, indexOIDs []uint32) []nailedRel {
	byOID := make(map[uint32]nailedRel, len(rels))
	for _, r := range rels {
		byOID[r.OID] = r
	}
	out := make([]nailedRel, 0, len(heapOIDs)+len(indexOIDs))
	for _, oid := range heapOIDs {
		if r, ok := byOID[oid]; ok {
			out = append(out, r)
		}
	}
	for _, oid := range indexOIDs {
		if r, ok := byOID[oid]; ok {
			out = append(out, r)
		}
	}
	return out
}

// padBufferTo4 appends zero bytes to buf until its length is a multiple of 4.
func padBufferTo4(buf *bytes.Buffer) {
	if rem := buf.Len() % 4; rem != 0 {
		buf.Write(make([]byte, 4-rem))
	}
}

// buildPgIndexUserData builds the user-data section of a pg_index HeapTuple.
// Columns 1-19 are written; columns 20-21 (indexprs, indpred) are NULL
// (reflected in the null bitmap — no bytes emitted for NULL cols).
// Variable-length fields (cols 16-19) are 4-byte aligned per PG's
// att_align_pointer convention for typalign='i'.
func buildPgIndexUserData(e pgIndexEntry) []byte {
	var buf bytes.Buffer
	natts := int16(len(e.IndKey))

	// Cols 1-2: OID (4 bytes each)
	binary.Write(&buf, binary.LittleEndian, e.IndexRelid) // 1: indexrelid
	binary.Write(&buf, binary.LittleEndian, e.IndRelid)   // 2: indrelid
	// Cols 3-4: int2 (2 bytes each)
	binary.Write(&buf, binary.LittleEndian, natts) // 3: indnatts
	binary.Write(&buf, binary.LittleEndian, natts) // 4: indnkeyatts
	// Cols 5-15: bool (1 byte each), ordered per pg_index.h
	for _, v := range []bool{
		e.IsUnique,  // 5:  indisunique
		false,       // 6:  indnullsnotdistinct
		e.IsPrimary, // 7:  indisprimary
		false,       // 8:  indisexclusion
		true,        // 9:  indimmediate
		false,       // 10: indisclustered
		true,        // 11: indisvalid
		false,       // 12: indcheckxmin
		true,        // 13: indisready
		true,        // 14: indislive
		false,       // 15: indisreplident
	} {
		if v {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	}
	// 23 bytes so far (4+4+2+2+11×1). Pad to 4 before first varlena.
	padBufferTo4(&buf) // → 24 bytes

	// Col 16: indkey (int2vector, 4-byte aligned varlena)
	buf.Write(int2VectorBytes(e.IndKey))
	padBufferTo4(&buf)

	// Col 17: indcollation (oidvector)
	buf.Write(oidVectorBytes(e.IndCollation))
	padBufferTo4(&buf)

	// Col 18: indclass (oidvector)
	buf.Write(oidVectorBytes(e.IndClass))
	padBufferTo4(&buf)

	// Col 19: indoption (int2vector); cols 20-21 are NULL — no bytes
	buf.Write(int2VectorBytes(e.IndOption))

	return buf.Bytes()
}

// buildPgIndexTupleBytes builds the binary representation of a pg_index
// HeapTuple as written to the relcache init file. Layout:
//
//	[HeapTupleData (heapTupleDataSize = 24 bytes)]
//	[HeapTupleHeaderData + null bitmap + padding (pgIndexTupleHoff = 32 bytes)]
//	[user data: pg_index cols 1-19]
//
// t_data is written as zeros; the reader fixes it up to point immediately
// after the HeapTupleData block (relcache.c:6328).
func buildPgIndexTupleBytes(e pgIndexEntry) []byte {
	userData := buildPgIndexUserData(e)
	tLen := uint32(pgIndexTupleHoff) + uint32(len(userData))

	var buf bytes.Buffer

	// HeapTupleData (24 bytes)
	binary.Write(&buf, binary.LittleEndian, tLen)         // t_len (4)
	buf.Write(make([]byte, 6))                             // t_self (6)
	binary.Write(&buf, binary.LittleEndian, uint32(2610)) // t_tableOid = pg_index (4)
	buf.Write(make([]byte, 2))                             // alignment padding (2)
	buf.Write(make([]byte, 8))                             // t_data pointer (8, fixed on read)

	// HeapTupleHeaderData (23 fixed bytes)
	binary.Write(&buf, binary.LittleEndian, uint32(1)) // t_xmin = Bootstrap XID (4)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // t_xmax = 0 (4)
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // t_cid = 0 (4)
	buf.Write(make([]byte, 6))                         // t_ctid (6)
	binary.Write(&buf, binary.LittleEndian, uint16(pgIndexNumCols)) // t_infomask2 = 21 (2)
	// t_infomask: HEAP_HASNULL(0x0001) | HEAP_HASVARWIDTH(0x0002) | HEAP_XMIN_COMMITTED(0x0100)
	binary.Write(&buf, binary.LittleEndian, uint16(0x0103)) // (2)
	buf.WriteByte(pgIndexTupleHoff)                         // t_hoff (1)

	// Null bitmap (3 bytes): cols 1-19 not-null (bit=1), cols 20-21 null (bit=0)
	buf.WriteByte(0xFF) // cols 1-8: all not-null
	buf.WriteByte(0xFF) // cols 9-16: all not-null
	buf.WriteByte(0x07) // cols 17-19 not-null (bits 0-2), cols 20-21 null (bits 3-4) = 0b00000111

	// Padding to pgIndexTupleHoff (= 32 - 23 - 3 = 6 bytes)
	buf.Write(make([]byte, pgIndexTupleHoff-pgIndexTupleHdrSize-pgIndexNullBitmapBytes))

	buf.Write(userData)
	return buf.Bytes()
}

// writePgIndexSubrecord appends the index sub-record for rel to buf.
// Format mirrors write_relcache_init_file (relcache.c:6702-6744):
//
//	indexTupleLen uint32  + indexTuple bytes
//	opfamilyLen   uint32  + opfamily   []uint32 (natts entries)
//	opcintypeLen  uint32  + opcintype  []uint32 (natts entries)
//	supportLen    uint32  + support    []uint32 (natts × btreeAmsupport entries)
//	indcollLen    uint32  + indcoll    []uint32 (natts entries)
//	indoptLen     uint32  + indoption  []int16  (natts entries)
//	per-col: opcoptLen uint32 (= 0 for standard btree indexes)
func writePgIndexSubrecord(
	buf *bytes.Buffer,
	rel nailedRel,
	entry pgIndexEntry,
	opcInfo map[uint32]opcClassInfo,
	amprocComp map[amprocCompKey]uint32,
) {
	natts := int(rel.RelNatts)

	// pg_index HeapTuple
	tup := buildPgIndexTupleBytes(entry)
	binary.Write(buf, binary.LittleEndian, uint32(len(tup)))
	buf.Write(tup)

	// Per-column arrays
	opfam := make([]byte, natts*4)
	opcintype := make([]byte, natts*4)
	support := make([]byte, natts*btreeAmsupport*4)
	indcoll := make([]byte, natts*4)
	indopt := make([]byte, natts*2)

	for i := 0; i < natts; i++ {
		info := opcInfo[entry.IndClass[i]]
		binary.LittleEndian.PutUint32(opfam[i*4:], info.family)
		binary.LittleEndian.PutUint32(opcintype[i*4:], info.intype)
		// Support proc #1 (comparison) at position i*btreeAmsupport;
		// slots 1-5 (procs 2-6) stay zero (optional/unsupported procs).
		cmpProc := amprocComp[amprocCompKey{family: info.family, intype: info.intype}]
		binary.LittleEndian.PutUint32(support[i*btreeAmsupport*4:], cmpProc)
		binary.LittleEndian.PutUint32(indcoll[i*4:], entry.IndCollation[i])
		binary.LittleEndian.PutUint16(indopt[i*2:], uint16(entry.IndOption[i]))
	}

	binary.Write(buf, binary.LittleEndian, uint32(len(opfam)))
	buf.Write(opfam)
	binary.Write(buf, binary.LittleEndian, uint32(len(opcintype)))
	buf.Write(opcintype)
	binary.Write(buf, binary.LittleEndian, uint32(len(support)))
	buf.Write(support)
	binary.Write(buf, binary.LittleEndian, uint32(len(indcoll)))
	buf.Write(indcoll)
	binary.Write(buf, binary.LittleEndian, uint32(len(indopt)))
	buf.Write(indopt)

	// opcoptions: all zero-length for standard btree indexes
	for i := 0; i < natts; i++ {
		binary.Write(buf, binary.LittleEndian, uint32(0))
	}
}

func writeRelcacheInitFile(dataDir string, shared bool, rels []nailedRel) error {
	// Restrict to exactly the canonical critical set required by
	// PG's trailing-count check (relcache.c:6510-6547).
	if shared {
		rels = filterCriticalRels(rels, criticalSharedHeapOIDs, criticalSharedIndexOIDs)
	} else {
		rels = filterCriticalRels(rels, criticalLocalHeapOIDs, criticalLocalIndexOIDs)
	}

	var path string
	if shared {
		path = filepath.Join(dataDir, "global", "pg_internal.init")
	} else {
		path = filepath.Join(dataDir, "base", "1", "pg_internal.init")
	}

	// Build index lookup tables once per call.
	opcInfo := buildOpcInfoByOID()
	amprocComp := buildAmprocCompByKey()
	idxByRelid := buildPgIndexEntryByRelid()

	var buf bytes.Buffer

	// Magic number (RELCACHE_INIT_FILEMAGIC = 0x573266, relcache.c:93)
	binary.Write(&buf, binary.LittleEndian, uint32(relCacheInitFileMagic))

	for _, rel := range rels {
		// 1. RelationData blob
		relData := buildRelationDataBlob(rel)
		binary.Write(&buf, binary.LittleEndian, uint32(len(relData)))
		buf.Write(relData)

		// 2. FormData_pg_class blob
		pgClass := buildPgClassBlob(rel)
		binary.Write(&buf, binary.LittleEndian, uint32(len(pgClass)))
		buf.Write(pgClass)

		// 3. FormData_pg_attribute for each column
		for _, a := range rel.Attrs {
			attrBlob := buildPgAttributeBlob(a)
			binary.Write(&buf, binary.LittleEndian, uint32(len(attrBlob)))
			buf.Write(attrBlob)
		}

		// 4. reloptions length (zero — no AM-specific options)
		binary.Write(&buf, binary.LittleEndian, uint32(0))

		// 5. Index sub-record — only for RELKIND_INDEX ('i').
		// Absent for heap rels; reader branches on relkind at :6305.
		if rel.RelKind == 'i' {
			entry, ok := idxByRelid[rel.OID]
			if !ok {
				return fmt.Errorf("writeRelcacheInitFile: no pg_index entry for OID %d", rel.OID)
			}
			writePgIndexSubrecord(&buf, rel, entry, opcInfo, amprocComp)
		}
	}

	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func buildRelationDataBlob(rel nailedRel) []byte {
	buf := make([]byte, sizeofRelationData)
	le := binary.LittleEndian

	// rd_id (offset 0, Oid, 4 bytes)
	le.PutUint32(buf[0:4], rel.OID)

	// We leave most fields zero. PG's load_relcache_init_file overwrites
	// many fields after loading (rd_rel from Form_pg_class, rd_att from
	// attributes, etc.). The critical fields are rd_id and rd_node.
	// Other zeroed fields will be filled by the loader.

	return buf
}

// buildPgClassBlob encodes a FormData_pg_class matching PG18 struct layout.
// Offsets verified against postgres/src/include/catalog/pg_class.h (PG18).
func buildPgClassBlob(rel nailedRel) []byte {
	buf := make([]byte, sizeofFormDataPgClass)
	le := binary.LittleEndian

	// oid (offset 0)
	le.PutUint32(buf[0:4], rel.OID)
	// relname (offset 4, NameData=64)
	copy(buf[4:4+pgNameDataLen], []byte(rel.RelName))
	// relnamespace (offset 68): PGNSP=11
	le.PutUint32(buf[68:72], 11)
	// reltype (offset 72)
	if rel.RelType != 0 {
		le.PutUint32(buf[72:76], rel.RelType)
	}
	// relowner (offset 80): bootstrap superuser = 10
	le.PutUint32(buf[80:84], 10)
	// relam (offset 84)
	if rel.RelKind == 'r' {
		le.PutUint32(buf[84:88], 2) // HEAP_TABLE_AM_OID
	} else if rel.RelKind == 'i' {
		le.PutUint32(buf[84:88], 403) // BTREE_AM_OID
	}
	// relfilenode (offset 88) = OID for nailed relations
	le.PutUint32(buf[88:92], rel.OID)
	// reltablespace (offset 92): M0106-0010 Step 3cr. Shared catalogs
	// must record GLOBALTABLESPACE_OID = 1664 so PG's
	// RelationInitPhysicalAddr (relcache.c:1347-1354) resolves the
	// file path to `global/<relfilenode>` instead of
	// `base/<MyDatabaseId>/<relfilenode>`. Local rels store 0.
	if rel.IsShared {
		le.PutUint32(buf[92:96], 1664)
	}
	// relpages (offset 96) = 0
	// reltuples (offset 100) = 0
	// relallvisible (offset 104) = 0
	// relallfrozen (offset 108) = 0
	// reltoastrelid (offset 112) = 0
	// relhasindex (offset 116)
	if rel.RelKind == 'r' {
		buf[116] = 1
	}
	// relisshared (offset 117)
	if rel.IsShared {
		buf[117] = 1
	}
	// relpersistence (offset 118): 'p'=permanent
	buf[118] = 'p'
	// relkind (offset 119): 'r'=relation, 'i'=index
	buf[119] = rel.RelKind
	// relnatts (offset 120)
	le.PutUint16(buf[120:122], uint16(rel.RelNatts))
	// relispopulated (offset 129): true
	buf[129] = 1
	// relispartition (offset 131): false
	// relfrozenxid (offset 136): 3 (FirstNormalTransactionId)
	le.PutUint32(buf[136:140], 3)
	// relminmxid (offset 140): 1 (FirstMultiXactId)
	le.PutUint32(buf[140:144], 1)

	return buf
}

func buildPgAttributeBlob(a nailedAttr) []byte {
	buf := make([]byte, attrFixedPartSize)
	le := binary.LittleEndian

	// We encode minimal attribute data. PG uses this to build tuple descriptors.
	// Key fields for load_relcache_init_file:
	// - attrelid (offset 0, Oid) — set by loader, not from file
	// - attname (offset 4, NameData=64 bytes)
	// - atttypid (offset 68, Oid)
	// - attlen (offset 72, int16)
	// - attnum (offset 74, int16)
	// - atttypmod (offset 76, int32) — -1
	// - attndims (offset 80, int16) — 0
	// - attbyval (offset 82, bool)
	// - attalign (offset 83, char)
	// - attstorage (offset 84, char) — 'p'=plain
	// - attcompression (offset 85, char) — '\0'
	// - attnotnull (offset 86, bool)
	// - atthasdef (offset 87, bool) — false
	// - atthasmissing (offset 88, bool) — false
	// - attidentity (offset 89, char) — '\0'
	// - attgenerated (offset 90, char) — '\0'
	// - attisdropped (offset 91, bool)
	// - attislocal (offset 92, bool) — true
	// - attinhcount (offset 93, int16) — 0
	// - attcollation (offset 96, Oid)

	nameBytes := []byte(a.Name)
	copy(buf[4:4+pgNameDataLen], nameBytes)
	if a.TypeOID != 0 {
		le.PutUint32(buf[68:72], a.TypeOID)
	}
	le.PutUint16(buf[74:76], uint16(a.Num))
	le.PutUint32(buf[76:80], 0xFFFFFFFF) // atttypmod = -1
	le.PutUint16(buf[72:74], uint16(a.Len))
	if a.NotNull {
		buf[86] = 1
	}
	buf[82] = pgTypeIsByVal(a.TypeOID) // attbyval
	buf[83] = pgAlignChar(a.Len)         // attalign
	buf[84] = 'p'                       // attstorage = 'p' (plain)
	buf[92] = 1                  // attislocal = true
	if a.IsDropped {
		buf[91] = 1
	}

	return buf
}

// pgTypeIsByVal returns 1 if the PG type OID is pass-by-value, 0 otherwise.
func pgTypeIsByVal(oid uint32) byte {
	switch oid {
	case 16, 18, 21, 23, 26, 28, 700, 20, 701:
		return 1
	}
	return 0
}

// pgAlignChar returns the PG alignment character for a given attlen.
func pgAlignChar(l int16) byte {
	switch {
	case l == 1:
		return 'c'
	case l == 2:
		return 's'
	case l == 4:
		return 'i'
	case l == 8:
		return 'd'
	case l == 64: // NameData
		return 'c'
	default:
		return 'i' // varlena and unknown
	}
}

// ---- Attribute lists for each nailed catalog ----

// pgDatabaseAttrs returns the 18-column PG18 pg_database schema. Must
// stay in lockstep with `internal/initdb/initdb.go::bootstrapPostgresDatabase`
// because PG's formrdesc-baked TupleDesc decodes the heap row through
// PG18's compile-time schema and any disagreement here either silently
// rots the goopg-internal view or surfaces as a Phase3 relcache
// assertion. Source: postgres/src/include/catalog/pg_database.h (M0106-0010
// Step 3ct).
func pgDatabaseAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "datname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "datdba", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "encoding", TypeOID: 23, Num: 4, Len: 4, NotNull: true},
		{Name: "datlocprovider", TypeOID: 18, Num: 5, Len: 1, NotNull: true},
		{Name: "datistemplate", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "datallowconn", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "dathasloginevt", TypeOID: 16, Num: 8, Len: 1, NotNull: true},
		{Name: "datconnlimit", TypeOID: 23, Num: 9, Len: 4, NotNull: true},
		{Name: "datfrozenxid", TypeOID: 28, Num: 10, Len: 4, NotNull: true},
		{Name: "datminmxid", TypeOID: 28, Num: 11, Len: 4, NotNull: true},
		{Name: "dattablespace", TypeOID: 26, Num: 12, Len: 4, NotNull: true},
		{Name: "datcollate", TypeOID: 25, Num: 13, Len: -1, NotNull: true},
		{Name: "datctype", TypeOID: 25, Num: 14, Len: -1, NotNull: true},
		{Name: "datlocale", TypeOID: 25, Num: 15, Len: -1},
		{Name: "daticurules", TypeOID: 25, Num: 16, Len: -1},
		{Name: "datcollversion", TypeOID: 25, Num: 17, Len: -1},
		{Name: "datacl", TypeOID: 1034, Num: 18, Len: -1},
	}
}

func pgAuthidAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "rolname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "rolsuper", TypeOID: 16, Num: 3, Len: 1, NotNull: true},
		{Name: "rolinherit", TypeOID: 16, Num: 4, Len: 1, NotNull: true},
		{Name: "rolcreaterole", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
		{Name: "rolcreatedb", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "rolcanlogin", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "rolreplication", TypeOID: 16, Num: 8, Len: 1, NotNull: true},
		{Name: "rolbypassrls", TypeOID: 16, Num: 9, Len: 1, NotNull: true},
		{Name: "rolconnlimit", TypeOID: 23, Num: 10, Len: 4, NotNull: true},
		{Name: "rolpassword", TypeOID: 25, Num: 11, Len: -1},
		{Name: "rolvaliduntil", TypeOID: 1184, Num: 12, Len: 8},
	}
}

func pgAuthMembersAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "roleid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "member", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "grantor", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "admin_option", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
	}
}

// pgShseclabelAttrs returns the 4-column PG18 pg_shseclabel schema
// (postgres/src/include/catalog/pg_shseclabel.h / schemapg.h
// Schema_pg_shseclabel). The catalog has objoid + classoid + provider +
// label and no `oid` column — it is BKI_BOOTSTRAP-without-oids. PG18's
// formrdesc("pg_shseclabel", Natts_pg_shseclabel=4, Desc_pg_shseclabel)
// builds a 4-column TupleDesc at Phase2; goopg must report relnatts=4
// in pg_class so write_relcache_init_file does not iterate past the
// rd_att array end and emit garbage attribute slots (M0106-0010 Step
// 3cv root cause).
func pgShseclabelAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "objoid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "classoid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "provider", TypeOID: 25, Num: 3, Len: -1, NotNull: true},
		{Name: "label", TypeOID: 25, Num: 4, Len: -1, NotNull: true},
	}
}

// pgSubscriptionAttrs returns the FULL 18-column PG18 pg_subscription schema
// (postgres/src/include/catalog/pg_subscription.h:43-97).
//
// M0131-S6 (2026-08-11) extended this from 9 columns to 18 and corrected
// substream's type. Two independent reasons:
//
//   - It was the sibling half of an already-PG-faithful writer.
//     PGSubscriptionColumnsPG18 (internal/executor/sys_pg_subscription.go) has
//     written all 18 columns since B4.4, and reloadSubscriptionsFromHeap reads
//     column 13/14/16 back — so goopg's own heap rows always carried 18
//     attributes while goopg's *self-description* claimed 9.
//   - It blocked M0131-S6's sixth view. pg_stat_subscription's verbatim
//     ev_action carries Vars up to `varattno 18` over relation 6100; with a
//     9-column pg_attribute a hosted PG fails rule expansion outright:
//     "cache lookup failed for attribute 10 of relation 6100".
//
// substream was TypeOID 16 (bool); upstream declares `char substream` and the
// writer already emits "f"/LOGICALREP_STREAM_OFF as a char. Values verified
// against a live PG 18.3 `initdb` (attnum/attname/atttypid/attlen/attnotnull
// read from its own pg_attribute), not transcribed from the header: the two
// nullable columns are subslotname (BKI_FORCE_NULL) and suborigin (BKI_DEFAULT
// with no FORCE_NOT_NULL), while subconninfo/subsynccommit/subpublications
// carry BKI_FORCE_NOT_NULL and are attnotnull.
func pgSubscriptionAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "subdbid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "subskiplsn", TypeOID: 3220, Num: 3, Len: 8, NotNull: true},
		{Name: "subname", TypeOID: 19, Num: 4, Len: 64, NotNull: true},
		{Name: "subowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "subenabled", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "subbinary", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "substream", TypeOID: 18, Num: 8, Len: 1, NotNull: true},
		{Name: "subtwophasestate", TypeOID: 18, Num: 9, Len: 1, NotNull: true},
		{Name: "subdisableonerr", TypeOID: 16, Num: 10, Len: 1, NotNull: true},
		{Name: "subpasswordrequired", TypeOID: 16, Num: 11, Len: 1, NotNull: true},
		{Name: "subrunasowner", TypeOID: 16, Num: 12, Len: 1, NotNull: true},
		{Name: "subfailover", TypeOID: 16, Num: 13, Len: 1, NotNull: true},
		// CATALOG_VARLEN block starts here.
		{Name: "subconninfo", TypeOID: 25, Num: 14, Len: -1, NotNull: true},
		{Name: "subslotname", TypeOID: 19, Num: 15, Len: 64, NotNull: false},
		{Name: "subsynccommit", TypeOID: 25, Num: 16, Len: -1, NotNull: true},
		{Name: "subpublications", TypeOID: 1009, Num: 17, Len: -1, NotNull: true},
		{Name: "suborigin", TypeOID: 25, Num: 18, Len: -1, NotNull: false},
	}
}

// pgSubscriptionRelAttrs returns the pg_subscription_rel column descriptors
// (M0106-0010 Step 3cg). Source: PG18
// `postgres/src/include/catalog/pg_subscription_rel.h:31`
// (`CATALOG(pg_subscription_rel,6102,SubscriptionRelRelationId)`) and
// `pg_subscription_rel_d.h` (Anum_pg_subscription_rel_* 1..4,
// Natts_pg_subscription_rel == 4). pg_subscription_rel has no `oid` system
// column — attnums start at 1 = srsubid. srsublsn is fixed-width pg_lsn
// (TypeOID 3220, 8 bytes) but BKI_FORCE_NULL inside CATALOG_VARLEN, so it
// must reflect NotNull=false in pg_attribute.
func pgSubscriptionRelAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "srsubid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "srrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "srsubstate", TypeOID: 18, Num: 3, Len: 1, NotNull: true},
		{Name: "srsublsn", TypeOID: 3220, Num: 4, Len: 8, NotNull: false},
	}
}

// pgTransformAttrs returns the pg_transform column descriptors
// (M0106-0010 Step 3ci). Source: PG18
// `postgres/src/include/catalog/pg_transform.h:29`
// (`CATALOG(pg_transform,3576,TransformRelationId)`) and
// `pg_transform_d.h` (Anum_pg_transform_* 1..5, Natts_pg_transform == 5).
// pg_transform DOES have an `oid` system column — attnum 1 = oid. All
// columns are fixed-width NOT NULL: trftype/trflang/trffromsql/trftosql
// are 4-byte OID columns. BKI_LOOKUP_OPT on trffromsql/trftosql means the
// pg_proc FK lookup is optional (stores 0 when the transform has no
// fromsql / tosql function), but the column itself is still NOT NULL.
func pgTransformAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "trftype", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "trflang", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "trffromsql", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "trftosql", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
	}
}

// pgTsConfigMapAttrs returns the pg_ts_config_map column descriptors
// (M0106-0010 Step 3cj). Source: PG18
// `postgres/src/include/catalog/pg_ts_config_map.h:30`
// (`CATALOG(pg_ts_config_map,3603,TSConfigMapRelationId)`) and
// `pg_ts_config_map_d.h` (Anum_pg_ts_config_map_* 1..4,
// Natts_pg_ts_config_map == 4). pg_ts_config_map has no `oid` system
// column — attnums start at 1 = mapcfg. All columns are fixed-width
// NOT NULL: mapcfg / mapdict are 4-byte OID columns (BKI_LOOKUP
// pg_ts_config / pg_ts_dict), maptokentype / mapseqno are 4-byte int4.
func pgTsConfigMapAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "mapcfg", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "maptokentype", TypeOID: 23, Num: 2, Len: 4, NotNull: true},
		{Name: "mapseqno", TypeOID: 23, Num: 3, Len: 4, NotNull: true},
		{Name: "mapdict", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
	}
}

// pgTsConfigAttrs returns the pg_ts_config column descriptors (M0106-0010
// Step 3ck). Source: PG18 `postgres/src/include/catalog/pg_ts_config.h:30`
// (`CATALOG(pg_ts_config,3602,TSConfigRelationId)`) and `pg_ts_config_d.h`
// (Anum_pg_ts_config_* 1..5, Natts_pg_ts_config == 5). pg_ts_config DOES
// have an `oid` system column — attnums start at 1 = oid. All columns
// are fixed-width NOT NULL: oid / cfgnamespace / cfgowner / cfgparser are
// 4-byte OID columns (cfgnamespace BKI_LOOKUP pg_namespace, cfgowner
// BKI_LOOKUP pg_authid, cfgparser BKI_LOOKUP pg_ts_parser); cfgname is
// the 64-byte NameData fixed string (typoid 19).
func pgTsConfigAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "cfgname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "cfgnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "cfgowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "cfgparser", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
	}
}

// pgTsDictAttrs returns the pg_ts_dict column descriptors (M0106-0010
// Step 3cm). Source: PG18 `postgres/src/include/catalog/pg_ts_dict.h:29`
// (`CATALOG(pg_ts_dict,3600,TSDictionaryRelationId)`) and
// `pg_ts_dict_d.h` (Anum_pg_ts_dict_* 1..6, Natts_pg_ts_dict == 6).
// pg_ts_dict DOES have an `oid` system column — attnums start at 1 = oid.
// Columns 1..5 are fixed-width NOT NULL: oid / dictnamespace / dictowner
// / dicttemplate are 4-byte OID columns (dictnamespace BKI_LOOKUP
// pg_namespace, dictowner BKI_LOOKUP pg_authid, dicttemplate BKI_LOOKUP
// pg_ts_template); dictname is the 64-byte NameData fixed string
// (typoid 19). Column 6 dictinitoption is a CATALOG_VARLEN text (typoid
// 25, varlena Len=-1) and is NULLABLE.
func pgTsDictAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "dictname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "dictnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "dictowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "dicttemplate", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "dictinitoption", TypeOID: 25, Num: 6, Len: -1, NotNull: false},
	}
}

// pgTsParserAttrs returns the pg_ts_parser column descriptors (M0106-0010
// Step 3cn). Source: PG18 `postgres/src/include/catalog/pg_ts_parser.h:29`
// (`CATALOG(pg_ts_parser,3601,TSParserRelationId)`) and `pg_ts_parser_d.h`
// (Anum_pg_ts_parser_* 1..8, Natts_pg_ts_parser == 8). pg_ts_parser DOES
// have an `oid` system column — attnums start at 1 = oid. All 8 columns
// are fixed-width NOT NULL: oid (26, 4-byte) + prsname (name 19, 64-byte
// NameData) + prsnamespace (oid 26, BKI_LOOKUP pg_namespace) + prsstart /
// prstoken / prsend / prsheadline / prslextype (regproc 24, all 4-byte).
// prsheadline is BKI_LOOKUP_OPT — the target proc may be InvalidOid but
// the column itself is NOT NULL (value 0 when absent).
func pgTsParserAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "prsname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "prsnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "prsstart", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "prstoken", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
		{Name: "prsend", TypeOID: 24, Num: 6, Len: 4, NotNull: true},
		{Name: "prsheadline", TypeOID: 24, Num: 7, Len: 4, NotNull: true},
		{Name: "prslextype", TypeOID: 24, Num: 8, Len: 4, NotNull: true},
	}
}

// pgTsTemplateAttrs returns the pg_ts_template column descriptors (M0106-0010
// Step 3co). Source: PG18 `postgres/src/include/catalog/pg_ts_template.h:29`
// (`CATALOG(pg_ts_template,3764,TSTemplateRelationId)`) and
// `pg_ts_template_d.h` (Anum_pg_ts_template_* 1..5, Natts_pg_ts_template == 5).
// pg_ts_template DOES have an `oid` system column — attnums start at 1 = oid.
// All 5 columns are fixed-width NOT NULL: oid (26, 4-byte) + tmplname
// (name 19, 64-byte NameData) + tmplnamespace (oid 26, BKI_LOOKUP
// pg_namespace) + tmplinit / tmpllexize (regproc 24, all 4-byte). tmplinit
// is BKI_LOOKUP_OPT — the target proc may be InvalidOid but the column
// itself is NOT NULL (value 0 when absent), mirroring prsheadline in
// pg_ts_parser.
func pgTsTemplateAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "tmplname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "tmplnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "tmplinit", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "tmpllexize", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
	}
}

// pgUserMappingAttrs returns the verbatim 4-column PG18 pg_user_mapping
// schema sourced from `postgres/src/include/catalog/pg_user_mapping.h` and
// pg_user_mapping_d.h (M0106-0010 step 3cp). Source:
//   CATALOG(pg_user_mapping,1418,UserMappingRelationId)
//   {
//     Oid  oid;
//     Oid  umuser   BKI_LOOKUP_OPT(pg_authid);
//     Oid  umserver BKI_LOOKUP(pg_foreign_server);
//     #ifdef CATALOG_VARLEN
//     text umoptions[1];
//     #endif
//   } FormData_pg_user_mapping;
// Three fixed-width 4-byte NOT NULL oid columns plus a CATALOG_VARLEN
// text[] (typeoid 1009) — umoptions is nullable. umuser is BKI_LOOKUP_OPT
// so InvalidOid (0) means PUBLIC; the column itself is still NOT NULL.
func pgUserMappingAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "umuser", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "umserver", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "umoptions", TypeOID: 1009, Num: 4, Len: -1, NotNull: false},
	}
}

func pgTypeAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "typname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "typnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "typowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "typlen", TypeOID: 21, Num: 5, Len: 2, NotNull: true},
		{Name: "typbyval", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "typtype", TypeOID: 18, Num: 7, Len: 1, NotNull: true},
		{Name: "typcategory", TypeOID: 18, Num: 8, Len: 1, NotNull: true},
		{Name: "typispreferred", TypeOID: 16, Num: 9, Len: 1, NotNull: true},
		{Name: "typisdefined", TypeOID: 16, Num: 10, Len: 1, NotNull: true},
		{Name: "typdelim", TypeOID: 18, Num: 11, Len: 1, NotNull: true},
		{Name: "typrelid", TypeOID: 26, Num: 12, Len: 4, NotNull: true},
		{Name: "typelem", TypeOID: 26, Num: 13, Len: 4, NotNull: true},
		{Name: "typarray", TypeOID: 26, Num: 14, Len: 4, NotNull: true},
	}
}

func pgClassAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "relname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "relnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "reltype", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "reloftype", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "relowner", TypeOID: 26, Num: 6, Len: 4, NotNull: true},
		{Name: "relam", TypeOID: 26, Num: 7, Len: 4, NotNull: true},
		{Name: "relfilenode", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
		{Name: "reltablespace", TypeOID: 26, Num: 9, Len: 4, NotNull: true},
		{Name: "relpages", TypeOID: 23, Num: 10, Len: 4, NotNull: true},
		{Name: "reltuples", TypeOID: 700, Num: 11, Len: 4, NotNull: true},
		{Name: "relallvisible", TypeOID: 23, Num: 12, Len: 4, NotNull: true},
		{Name: "relallfrozen", TypeOID: 23, Num: 13, Len: 4, NotNull: true},
		{Name: "reltoastrelid", TypeOID: 26, Num: 14, Len: 4, NotNull: true},
		{Name: "relhasindex", TypeOID: 16, Num: 15, Len: 1, NotNull: true},
		{Name: "relisshared", TypeOID: 16, Num: 16, Len: 1, NotNull: true},
		{Name: "relpersistence", TypeOID: 18, Num: 17, Len: 1, NotNull: true},
		{Name: "relkind", TypeOID: 18, Num: 18, Len: 1, NotNull: true},
		{Name: "relnatts", TypeOID: 21, Num: 19, Len: 2, NotNull: true},
		{Name: "relchecks", TypeOID: 21, Num: 20, Len: 2, NotNull: true},
		{Name: "relhasrules", TypeOID: 16, Num: 21, Len: 1},
		{Name: "relhastriggers", TypeOID: 16, Num: 22, Len: 1},
		{Name: "relhassubclass", TypeOID: 16, Num: 23, Len: 1},
		{Name: "relrowsecurity", TypeOID: 16, Num: 24, Len: 1},
		{Name: "relforcerowsecurity", TypeOID: 16, Num: 25, Len: 1},
		{Name: "relispopulated", TypeOID: 16, Num: 26, Len: 1, NotNull: true},
		{Name: "relreplident", TypeOID: 18, Num: 27, Len: 1, NotNull: true},
		{Name: "relispartition", TypeOID: 16, Num: 28, Len: 1, NotNull: true},
		{Name: "relrewrite", TypeOID: 26, Num: 29, Len: 4, NotNull: true},
		{Name: "relfrozenxid", TypeOID: 28, Num: 30, Len: 4, NotNull: true},
		{Name: "relminmxid", TypeOID: 28, Num: 31, Len: 4, NotNull: true},
		{Name: "relacl", TypeOID: 1034, Num: 32, Len: -1},
		{Name: "reloptions", TypeOID: 1009, Num: 33, Len: -1},
		{Name: "relpartbound", TypeOID: 194, Num: 34, Len: -1},
	}
}

func pgAttributeAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "attrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "attname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "atttypid", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "attstattarget", TypeOID: 21, Num: 4, Len: 2},
		{Name: "attlen", TypeOID: 21, Num: 5, Len: 2, NotNull: true},
		{Name: "attnum", TypeOID: 21, Num: 6, Len: 2, NotNull: true},
		{Name: "attndims", TypeOID: 21, Num: 7, Len: 2, NotNull: true},
		{Name: "attcacheoff", TypeOID: 23, Num: 8, Len: 4, NotNull: true},
		{Name: "atttypmod", TypeOID: 23, Num: 9, Len: 4, NotNull: true},
		{Name: "attbyval", TypeOID: 16, Num: 10, Len: 1, NotNull: true},
		{Name: "attalign", TypeOID: 18, Num: 11, Len: 1, NotNull: true},
		{Name: "attstorage", TypeOID: 18, Num: 12, Len: 1, NotNull: true},
		{Name: "attcompression", TypeOID: 18, Num: 13, Len: 1, NotNull: true},
		{Name: "attnotnull", TypeOID: 16, Num: 14, Len: 1, NotNull: true},
		{Name: "atthasdef", TypeOID: 16, Num: 15, Len: 1, NotNull: true},
		{Name: "atthasmissing", TypeOID: 16, Num: 16, Len: 1, NotNull: true},
		{Name: "attidentity", TypeOID: 18, Num: 17, Len: 1, NotNull: true},
		{Name: "attgenerated", TypeOID: 18, Num: 18, Len: 1, NotNull: true},
		{Name: "attisdropped", TypeOID: 16, Num: 19, Len: 1, NotNull: true},
		{Name: "attislocal", TypeOID: 16, Num: 20, Len: 1, NotNull: true},
		{Name: "attinhcount", TypeOID: 21, Num: 21, Len: 2, NotNull: true},
		{Name: "attcollation", TypeOID: 26, Num: 22, Len: 4, NotNull: true},
		{Name: "attacl", TypeOID: 1034, Num: 23, Len: -1},
		{Name: "attoptions", TypeOID: 25, Num: 24, Len: -1},
	}
}

func pgProcAttrs() []nailedAttr {
	// PG18 FormData_pg_proc — 30 columns. Column order, OIDs and
	// per-attr (Len, NotNull) flags match
	// `postgres/src/include/catalog/pg_proc.h`. M0106-0010 step 3a
	// bumps relnatts 13 → 30 so the init-file TupleDesc agrees with
	// the heap-tuple seed produced by bootstrapPgProcTuples.
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "proname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "pronamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "proowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "prolang", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "procost", TypeOID: 700, Num: 6, Len: 4, NotNull: true},
		{Name: "prorows", TypeOID: 700, Num: 7, Len: 4, NotNull: true},
		{Name: "provariadic", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
		{Name: "prosupport", TypeOID: 24, Num: 9, Len: 4, NotNull: true},
		{Name: "prokind", TypeOID: 18, Num: 10, Len: 1, NotNull: true},
		{Name: "prosecdef", TypeOID: 16, Num: 11, Len: 1, NotNull: true},
		{Name: "proleakproof", TypeOID: 16, Num: 12, Len: 1, NotNull: true},
		{Name: "proisstrict", TypeOID: 16, Num: 13, Len: 1, NotNull: true},
		{Name: "proretset", TypeOID: 16, Num: 14, Len: 1, NotNull: true},
		{Name: "provolatile", TypeOID: 18, Num: 15, Len: 1, NotNull: true},
		{Name: "proparallel", TypeOID: 18, Num: 16, Len: 1, NotNull: true},
		{Name: "pronargs", TypeOID: 21, Num: 17, Len: 2, NotNull: true},
		{Name: "pronargdefaults", TypeOID: 21, Num: 18, Len: 2, NotNull: true},
		{Name: "prorettype", TypeOID: 26, Num: 19, Len: 4, NotNull: true},
		{Name: "proargtypes", TypeOID: 30, Num: 20, Len: -1, NotNull: true},
		// CATALOG_VARLEN fields — nullable in PG; goopg encodes
		// empty binary placeholders (see encodeValuePG) so PG's
		// raw-bytes-as-ArrayType dereferences do not crash.
		{Name: "proallargtypes", TypeOID: 1028, Num: 21, Len: -1, NotNull: false},
		{Name: "proargmodes", TypeOID: 1002, Num: 22, Len: -1, NotNull: false},
		{Name: "proargnames", TypeOID: 1009, Num: 23, Len: -1, NotNull: false},
		{Name: "proargdefaults", TypeOID: 194, Num: 24, Len: -1, NotNull: false},
		{Name: "protrftypes", TypeOID: 1028, Num: 25, Len: -1, NotNull: false},
		{Name: "prosrc", TypeOID: 25, Num: 26, Len: -1, NotNull: true},
		{Name: "probin", TypeOID: 25, Num: 27, Len: -1, NotNull: false},
		{Name: "prosqlbody", TypeOID: 194, Num: 28, Len: -1, NotNull: false},
		{Name: "proconfig", TypeOID: 1009, Num: 29, Len: -1, NotNull: false},
		{Name: "proacl", TypeOID: 1034, Num: 30, Len: -1, NotNull: false},
	}
}

func pgIndexAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "indexrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "indrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "indnatts", TypeOID: 21, Num: 3, Len: 2, NotNull: true},
		{Name: "indnkeyatts", TypeOID: 21, Num: 4, Len: 2, NotNull: true},
		{Name: "indisunique", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
		{Name: "indnullsnotdistinct", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "indisprimary", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "indisexclusion", TypeOID: 16, Num: 8, Len: 1, NotNull: true},
		{Name: "indimmediate", TypeOID: 16, Num: 9, Len: 1, NotNull: true},
		{Name: "indisclustered", TypeOID: 16, Num: 10, Len: 1, NotNull: true},
		{Name: "indisvalid", TypeOID: 16, Num: 11, Len: 1, NotNull: true},
		{Name: "indcheckxmin", TypeOID: 16, Num: 12, Len: 1, NotNull: true},
		{Name: "indisready", TypeOID: 16, Num: 13, Len: 1, NotNull: true},
		{Name: "indislive", TypeOID: 16, Num: 14, Len: 1, NotNull: true},
		{Name: "indisreplident", TypeOID: 16, Num: 15, Len: 1, NotNull: true},
		// Variable-length region. int2vector indkey is BKI_FORCE_NOT_NULL.
		{Name: "indkey", TypeOID: 22, Num: 16, Len: -1, NotNull: true},
		{Name: "indcollation", TypeOID: 30, Num: 17, Len: -1, NotNull: true},
		{Name: "indclass", TypeOID: 30, Num: 18, Len: -1, NotNull: true},
		{Name: "indoption", TypeOID: 22, Num: 19, Len: -1, NotNull: true},
		// indexprs / indpred are pg_node_tree and nullable.
		{Name: "indexprs", TypeOID: 194, Num: 20, Len: -1, NotNull: false},
		{Name: "indpred", TypeOID: 194, Num: 21, Len: -1, NotNull: false},
	}
}

func pgOpclassAttrs() []nailedAttr {
	// M0106-0010 step 3b: expanded to the full PG18 FormData_pg_opclass
	// column set so PG's heap_deformtuple can read opcdefault / opckeytype
	// when SearchSysCache1(CLAOID, ...) returns a row.
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "opcmethod", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "opcname", TypeOID: 19, Num: 3, Len: 64, NotNull: true},
		{Name: "opcnamespace", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "opcowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "opcfamily", TypeOID: 26, Num: 6, Len: 4, NotNull: true},
		{Name: "opcintype", TypeOID: 26, Num: 7, Len: 4, NotNull: true},
		{Name: "opcdefault", TypeOID: 16, Num: 8, Len: 1, NotNull: true},
		{Name: "opckeytype", TypeOID: 26, Num: 9, Len: 4, NotNull: true},
	}
}

func pgAmprocAttrs() []nailedAttr {
	// M0106-0010 step 3c: PG18 FormData_pg_amproc has 6 columns.
	// Heap-tuple seed in initdb writes all 6; the init file's
	// TupleDesc must agree so heap_deformtuple can read amprocnum
	// (attnum 5) and amproc (regproc, attnum 6).
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "amprocfamily", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "amproclefttype", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "amprocrighttype", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "amprocnum", TypeOID: 21, Num: 5, Len: 2, NotNull: true},
		{Name: "amproc", TypeOID: 24, Num: 6, Len: 4, NotNull: true},
	}
}

// pgRewriteAttrs returns the 8 PG18-canonical pg_attribute rows for
// `pg_catalog.pg_rewrite`. Column order, names, and types are verbatim
// from `postgres/src/include/catalog/pg_rewrite.h:32-44`. ev_qual and
// ev_action are stored as `pg_node_tree` (TypeOID 194, varlena) with
// BKI_FORCE_NOT_NULL. is_instead is the PG18 boolean column omitted by
// the pre-Step-3dm 7-column layout (the prior layout's `ev_owner` does
// not exist in PG18 at all; rule ownership is tracked via the owning
// relation's pg_class.relowner). The index entry at
// `initdb.go::bootstrapPgIndexTuples` for pg_rewrite_rel_rulename_index
// already assumes this canonical layout (indkey=[3,2] = ev_class,
// rulename); fixing the schema makes the TupleDesc and the index
// agree.
func pgRewriteAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "rulename", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "ev_class", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "ev_type", TypeOID: 18, Num: 4, Len: 1, NotNull: true},
		{Name: "ev_enabled", TypeOID: 18, Num: 5, Len: 1, NotNull: true},
		{Name: "is_instead", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "ev_qual", TypeOID: 194, Num: 7, Len: -1, NotNull: true},
		{Name: "ev_action", TypeOID: 194, Num: 8, Len: -1, NotNull: true},
	}
}


// pgStatWalReceiverAttrs returns the 15 pg_attribute rows for the
// `pg_catalog.pg_stat_wal_receiver` view (M0106-0010 Step 3dl). Column
// order, names, and types are verbatim from
// `postgres/src/backend/catalog/system_views.sql:945-963`, which
// projects all 15 OUT-args of `pg_stat_get_wal_receiver()` (pg_proc OID
// 3317, see Step 3dj/3dk). View columns inherit nullability from the
// underlying expression so pg_attribute.attnotnull is false even for
// fixed-width columns.
//
// Type OID legend (verbatim from `postgres/src/include/catalog/pg_proc.dat:5671`):
//
//	int4 = 23, text = 25, pg_lsn = 3220, timestamptz = 1184
func pgStatWalReceiverAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "pid", TypeOID: 23, Num: 1, Len: 4},
		{Name: "status", TypeOID: 25, Num: 2, Len: -1},
		{Name: "receive_start_lsn", TypeOID: 3220, Num: 3, Len: 8},
		{Name: "receive_start_tli", TypeOID: 23, Num: 4, Len: 4},
		{Name: "written_lsn", TypeOID: 3220, Num: 5, Len: 8},
		{Name: "flushed_lsn", TypeOID: 3220, Num: 6, Len: 8},
		{Name: "received_tli", TypeOID: 23, Num: 7, Len: 4},
		{Name: "last_msg_send_time", TypeOID: 1184, Num: 8, Len: 8},
		{Name: "last_msg_receipt_time", TypeOID: 1184, Num: 9, Len: 8},
		{Name: "latest_end_lsn", TypeOID: 3220, Num: 10, Len: 8},
		{Name: "latest_end_time", TypeOID: 1184, Num: 11, Len: 8},
		{Name: "slot_name", TypeOID: 25, Num: 12, Len: -1},
		{Name: "sender_host", TypeOID: 25, Num: 13, Len: -1},
		{Name: "sender_port", TypeOID: 23, Num: 14, Len: 4},
		{Name: "conninfo", TypeOID: 25, Num: 15, Len: -1},
	}
}

// pgStatReplicationViewAttrs returns the 20 pg_attribute rows for the
// pg_catalog.pg_stat_replication view (OID 12231, batched-28).
// Column order and types from system_views.sql:906-930 + pg_proc.dat (OID 3099).
// View columns inherit nullability from the underlying expression → NotNull=false.
func pgStatReplicationViewAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "pid", TypeOID: 23, Num: 1, Len: 4},
		{Name: "usesysid", TypeOID: 26, Num: 2, Len: 4},
		{Name: "usename", TypeOID: 19, Num: 3, Len: 64},
		{Name: "application_name", TypeOID: 25, Num: 4, Len: -1},
		{Name: "client_addr", TypeOID: 869, Num: 5, Len: -1},
		{Name: "client_hostname", TypeOID: 25, Num: 6, Len: -1},
		{Name: "client_port", TypeOID: 23, Num: 7, Len: 4},
		{Name: "backend_start", TypeOID: 1184, Num: 8, Len: 8},
		{Name: "backend_xmin", TypeOID: 28, Num: 9, Len: 4},
		{Name: "state", TypeOID: 25, Num: 10, Len: -1},
		{Name: "sent_lsn", TypeOID: 3220, Num: 11, Len: 8},
		{Name: "write_lsn", TypeOID: 3220, Num: 12, Len: 8},
		{Name: "flush_lsn", TypeOID: 3220, Num: 13, Len: 8},
		{Name: "replay_lsn", TypeOID: 3220, Num: 14, Len: 8},
		{Name: "write_lag", TypeOID: 1186, Num: 15, Len: 16},
		{Name: "flush_lag", TypeOID: 1186, Num: 16, Len: 16},
		{Name: "replay_lag", TypeOID: 1186, Num: 17, Len: 16},
		{Name: "sync_priority", TypeOID: 23, Num: 18, Len: 4},
		{Name: "sync_state", TypeOID: 25, Num: 19, Len: -1},
		{Name: "reply_time", TypeOID: 1184, Num: 20, Len: 8},
	}
}

// pgStatRecoveryPrefetchViewAttrs returns the 10 pg_attribute rows for the
// pg_catalog.pg_stat_recovery_prefetch view (OID 12244, batched-28).
// Column order and types from system_views.sql:965-977 + pg_proc.dat (OID 6248).
func pgStatRecoveryPrefetchViewAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "stats_reset", TypeOID: 1184, Num: 1, Len: 8},
		{Name: "prefetch", TypeOID: 20, Num: 2, Len: 8},
		{Name: "hit", TypeOID: 20, Num: 3, Len: 8},
		{Name: "skip_init", TypeOID: 20, Num: 4, Len: 8},
		{Name: "skip_new", TypeOID: 20, Num: 5, Len: 8},
		{Name: "skip_fpw", TypeOID: 20, Num: 6, Len: 8},
		{Name: "skip_rep", TypeOID: 20, Num: 7, Len: 8},
		{Name: "wal_distance", TypeOID: 23, Num: 8, Len: 4},
		{Name: "block_distance", TypeOID: 23, Num: 9, Len: 4},
		{Name: "io_depth", TypeOID: 23, Num: 10, Len: 4},
	}
}

// pgStatSubscriptionViewAttrs returns the 11 pg_attribute rows for the
// pg_catalog.pg_stat_subscription view (OID 12248, batched-28).
// Column order and types from system_views.sql:979-994 + pg_proc.dat (OID 6118).
func pgStatSubscriptionViewAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "subid", TypeOID: 26, Num: 1, Len: 4},
		{Name: "subname", TypeOID: 19, Num: 2, Len: 64},
		{Name: "worker_type", TypeOID: 25, Num: 3, Len: -1},
		{Name: "pid", TypeOID: 23, Num: 4, Len: 4},
		{Name: "leader_pid", TypeOID: 23, Num: 5, Len: 4},
		{Name: "relid", TypeOID: 26, Num: 6, Len: 4},
		{Name: "received_lsn", TypeOID: 3220, Num: 7, Len: 8},
		{Name: "last_msg_send_time", TypeOID: 1184, Num: 8, Len: 8},
		{Name: "last_msg_receipt_time", TypeOID: 1184, Num: 9, Len: 8},
		{Name: "latest_end_lsn", TypeOID: 3220, Num: 10, Len: 8},
		{Name: "latest_end_time", TypeOID: 1184, Num: 11, Len: 8},
	}
}

// pgReplicationSlotsViewAttrs returns the 21 pg_attribute rows for the
// pg_catalog.pg_replication_slots view (OID 12261, batched-28).
// Column order and types from system_views.sql:1019-1043 + pg_proc.dat (OID 3781).
// PG18 adds two_phase_at, inactive_since, conflicting, invalidation_reason,
// failover, synced as the last six entries.
func pgReplicationSlotsViewAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "slot_name", TypeOID: 19, Num: 1, Len: 64},
		{Name: "plugin", TypeOID: 19, Num: 2, Len: 64},
		{Name: "slot_type", TypeOID: 25, Num: 3, Len: -1},
		{Name: "datoid", TypeOID: 26, Num: 4, Len: 4},
		{Name: "database", TypeOID: 19, Num: 5, Len: 64},
		{Name: "temporary", TypeOID: 16, Num: 6, Len: 1},
		{Name: "active", TypeOID: 16, Num: 7, Len: 1},
		{Name: "active_pid", TypeOID: 23, Num: 8, Len: 4},
		{Name: "xmin", TypeOID: 28, Num: 9, Len: 4},
		{Name: "catalog_xmin", TypeOID: 28, Num: 10, Len: 4},
		{Name: "restart_lsn", TypeOID: 3220, Num: 11, Len: 8},
		{Name: "confirmed_flush_lsn", TypeOID: 3220, Num: 12, Len: 8},
		{Name: "wal_status", TypeOID: 25, Num: 13, Len: -1},
		{Name: "safe_wal_size", TypeOID: 20, Num: 14, Len: 8},
		{Name: "two_phase", TypeOID: 16, Num: 15, Len: 1},
		{Name: "two_phase_at", TypeOID: 3220, Num: 16, Len: 8},
		{Name: "inactive_since", TypeOID: 1184, Num: 17, Len: 8},
		{Name: "conflicting", TypeOID: 16, Num: 18, Len: 1},
		{Name: "invalidation_reason", TypeOID: 25, Num: 19, Len: -1},
		{Name: "failover", TypeOID: 16, Num: 20, Len: 1},
		{Name: "synced", TypeOID: 16, Num: 21, Len: 1},
	}
}

// pgStatReplicationSlotsViewAttrs returns the 10 pg_attribute rows for the
// pg_catalog.pg_stat_replication_slots view (OID 12266, batched-28).
// Column order and types from system_views.sql:1045-1059 + pg_proc.dat (OID 6169).
func pgStatReplicationSlotsViewAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "slot_name", TypeOID: 19, Num: 1, Len: 64},
		{Name: "spill_txns", TypeOID: 20, Num: 2, Len: 8},
		{Name: "spill_count", TypeOID: 20, Num: 3, Len: 8},
		{Name: "spill_bytes", TypeOID: 20, Num: 4, Len: 8},
		{Name: "stream_txns", TypeOID: 20, Num: 5, Len: 8},
		{Name: "stream_count", TypeOID: 20, Num: 6, Len: 8},
		{Name: "stream_bytes", TypeOID: 20, Num: 7, Len: 8},
		{Name: "total_txns", TypeOID: 20, Num: 8, Len: 8},
		{Name: "total_bytes", TypeOID: 20, Num: 9, Len: 8},
		{Name: "stats_reset", TypeOID: 1184, Num: 10, Len: 8},
	}
}

func pgTriggerAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "tgrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "tgname", TypeOID: 19, Num: 3, Len: 64, NotNull: true},
		{Name: "tgfoid", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "tgtype", TypeOID: 21, Num: 5, Len: 2, NotNull: true},
		{Name: "tgenabled", TypeOID: 18, Num: 6, Len: 1, NotNull: true},
		{Name: "tgisinternal", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "tgconstrrelid", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
	}
}

func pgAmAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "amname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "amhandler", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		// M0106-0010 step 2: PG18 FormData_pg_am declares 4 columns.
		// Heap-tuple seed in initdb writes a 1-byte char at the
		// trailing slot; the init file's TupleDesc must agree.
		{Name: "amtype", TypeOID: 18, Num: 4, Len: 1, NotNull: true},
	}
}

func pgOperatorAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "oprname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "oprnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "oprowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "oprkind", TypeOID: 18, Num: 5, Len: 1, NotNull: true},
		{Name: "oprcanmerge", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "oprcanhash", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "oprleft", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
		{Name: "oprright", TypeOID: 26, Num: 9, Len: 4, NotNull: true},
		{Name: "oprresult", TypeOID: 26, Num: 10, Len: 4, NotNull: true},
	}
}

func pgInheritsAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "inhrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "inhparent", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "inhseqno", TypeOID: 23, Num: 3, Len: 4, NotNull: true},
	}
}

func pgAmopAttrs() []nailedAttr {
	// M0106-0010 step 3c: PG18 FormData_pg_amop has 9 columns.
	// Heap-tuple seed in initdb writes all 9; the init file's
	// TupleDesc must agree so heap_deformtuple can read every
	// attr (amopopr at attnum 7 in particular).
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "amopfamily", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "amoplefttype", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "amoprighttype", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "amopstrategy", TypeOID: 21, Num: 5, Len: 2, NotNull: true},
		{Name: "amoppurpose", TypeOID: 18, Num: 6, Len: 1, NotNull: true},
		{Name: "amopopr", TypeOID: 26, Num: 7, Len: 4, NotNull: true},
		{Name: "amopmethod", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
		{Name: "amopsortfamily", TypeOID: 26, Num: 9, Len: 4, NotNull: true},
	}
}

func pgCollationAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "collname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "collnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "collowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "collprovider", TypeOID: 18, Num: 5, Len: 1, NotNull: true},
		{Name: "collisdeterministic", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "collencoding", TypeOID: 23, Num: 7, Len: 4, NotNull: true},
		{Name: "collcollate", TypeOID: 19, Num: 8, Len: 64, NotNull: true},
	}
}

func pgLanguageAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "lanname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "lanowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "lanispl", TypeOID: 16, Num: 4, Len: 1, NotNull: true},
		{Name: "lanpltrusted", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
		{Name: "lanplcallfoid", TypeOID: 26, Num: 6, Len: 4, NotNull: true},
		{Name: "laninline", TypeOID: 26, Num: 7, Len: 4, NotNull: true},
	}
}

func pgConstraintAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "conname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "connamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "contype", TypeOID: 18, Num: 4, Len: 1, NotNull: true},
		{Name: "condeferrable", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
		{Name: "condeferred", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "convalidated", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "conrelid", TypeOID: 26, Num: 8, Len: 4, NotNull: true},
		{Name: "contypid", TypeOID: 26, Num: 9, Len: 4, NotNull: true},
		{Name: "conindid", TypeOID: 26, Num: 10, Len: 4, NotNull: true},
		{Name: "confrelid", TypeOID: 26, Num: 11, Len: 4, NotNull: true},
	}
}

func pgNamespaceAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "nspname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "nspowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "nspacl", TypeOID: 1034, Num: 4, Len: -1},
		{Name: "nspblocked", TypeOID: 16, Num: 5, Len: 1},
	}
}

// pgAttrdefAttrs mirrors FormData_pg_attrdef
// (postgres/src/include/catalog/pg_attrdef.h):
//
//	1 oid      oid           (NOT NULL)
//	2 adrelid  oid           (NOT NULL)
//	3 adnum    int2          (NOT NULL)
//	4 adbin    pg_node_tree  (BKI_FORCE_NOT_NULL, CATALOG_VARLEN)
//
// M-NIGHTLY AI-20260810-011258-003: `adbin` was missing here even though
// goopg's heap writer (PGAttrdefColumnsPG18 / writeAttrdefRow) has always
// written a 4-column row. pg_attrdef is NOT formrdesc'd, so a real PG 18.3
// standby rebuilds its TupleDesc from the streamed pg_attribute rows for
// relid 2604 — with only three attributes the standby's relcache saw no
// `adbin` column at all (a direct `pg_get_expr(adbin, adrelid)` failed with
// `column "adbin" does not exist`) and AttrDefaultFetch could not read the
// expression it had just replayed.
func pgAttrdefAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "adrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "adnum", TypeOID: 21, Num: 3, Len: 2, NotNull: true},
		{Name: "adbin", TypeOID: 194, Num: 4, Len: -1, NotNull: true},
	}
}

func pgDescriptionAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "objoid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "classoid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "objsubid", TypeOID: 23, Num: 3, Len: 4, NotNull: true},
		{Name: "description", TypeOID: 25, Num: 4, Len: -1, NotNull: true},
		{Name: "shared", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
	}
}

func pgDependAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "classid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "objid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "objsubid", TypeOID: 23, Num: 3, Len: 4, NotNull: true},
		{Name: "refclassid", TypeOID: 26, Num: 4, Len: 4, NotNull: true},
		{Name: "refobjid", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "refobjsubid", TypeOID: 23, Num: 6, Len: 4, NotNull: true},
		{Name: "deptype", TypeOID: 18, Num: 7, Len: 1, NotNull: true},
		{Name: "objversion", TypeOID: 28, Num: 8, Len: 4, NotNull: true},
	}
}

// pgAggregateAttrs defines the 22-column PG18 pg_aggregate schema. PG
// doesn't formrdesc pg_aggregate, so RelType is free (we use 83 like the
// other non-formrdesc'd local nailed catalogs). Schema sourced from
// `postgres/src/include/catalog/pg_aggregate_d.h` (Anum_pg_aggregate_*)
// and `pg_aggregate.h` (column type declarations).
//
// Why this exists: M0106-0010 step 3w. After Step 3v cleared the relcache
// init-file PANIC loop, PG standby's backends FATAL with
// `could not open relation with OID 2600` from
// `relation.c::table_openrv` — `RelationIdGetRelation(2600)` returns NULL
// because `RelationBuildDesc → ScanPgRelation(2600)` finds no pg_class
// row. Adding pg_aggregate to nailedLocalRels writes the row via
// `bootstrapPgClassTuples`, lays down 22 pg_attribute rows via
// `bootstrapPgAttributeTuples`, and threads its heap-TID into the
// pg_class_oid_index btree.
func pgAggregateAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "aggfnoid", TypeOID: 24, Num: 1, Len: 4, NotNull: true},      // regproc
		{Name: "aggkind", TypeOID: 18, Num: 2, Len: 1, NotNull: true},       // char
		{Name: "aggnumdirectargs", TypeOID: 21, Num: 3, Len: 2, NotNull: true}, // int2
		{Name: "aggtransfn", TypeOID: 24, Num: 4, Len: 4, NotNull: true},
		{Name: "aggfinalfn", TypeOID: 24, Num: 5, Len: 4, NotNull: true},
		{Name: "aggcombinefn", TypeOID: 24, Num: 6, Len: 4, NotNull: true},
		{Name: "aggserialfn", TypeOID: 24, Num: 7, Len: 4, NotNull: true},
		{Name: "aggdeserialfn", TypeOID: 24, Num: 8, Len: 4, NotNull: true},
		{Name: "aggmtransfn", TypeOID: 24, Num: 9, Len: 4, NotNull: true},
		{Name: "aggminvtransfn", TypeOID: 24, Num: 10, Len: 4, NotNull: true},
		{Name: "aggmfinalfn", TypeOID: 24, Num: 11, Len: 4, NotNull: true},
		{Name: "aggfinalextra", TypeOID: 16, Num: 12, Len: 1, NotNull: true}, // bool
		{Name: "aggmfinalextra", TypeOID: 16, Num: 13, Len: 1, NotNull: true},
		{Name: "aggfinalmodify", TypeOID: 18, Num: 14, Len: 1, NotNull: true},
		{Name: "aggmfinalmodify", TypeOID: 18, Num: 15, Len: 1, NotNull: true},
		{Name: "aggsortop", TypeOID: 26, Num: 16, Len: 4, NotNull: true}, // oid
		{Name: "aggtranstype", TypeOID: 26, Num: 17, Len: 4, NotNull: true},
		{Name: "aggtransspace", TypeOID: 23, Num: 18, Len: 4, NotNull: true}, // int4
		{Name: "aggmtranstype", TypeOID: 26, Num: 19, Len: 4, NotNull: true},
		{Name: "aggmtransspace", TypeOID: 23, Num: 20, Len: 4, NotNull: true},
		{Name: "agginitval", TypeOID: 25, Num: 21, Len: -1},                  // text (nullable)
		{Name: "aggminitval", TypeOID: 25, Num: 22, Len: -1},                 // text (nullable)
	}
}

// pgCastAttrs defines the 6-column PG18 pg_cast schema. PG
// `RelationBuildDesc(2605) → ScanPgRelation(2605)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 2605`. Sourced verbatim from `postgres/src/include/catalog/pg_cast.h`
// and `pg_cast_d.h` (Anum_pg_cast_* 1–6). M0106-0010 step 3aa.
func pgCastAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},         // oid
		{Name: "castsource", TypeOID: 26, Num: 2, Len: 4, NotNull: true},  // oid → pg_type
		{Name: "casttarget", TypeOID: 26, Num: 3, Len: 4, NotNull: true},  // oid → pg_type
		{Name: "castfunc", TypeOID: 26, Num: 4, Len: 4, NotNull: true},    // oid → pg_proc (0 = binary)
		{Name: "castcontext", TypeOID: 18, Num: 5, Len: 1, NotNull: true}, // char (i/a/e)
		{Name: "castmethod", TypeOID: 18, Num: 6, Len: 1, NotNull: true},  // char (f/b/i)
	}
}

// pgConversionAttrs defines the 8-column PG18 pg_conversion schema.
// PG `RelationBuildDesc(2607) → ScanPgRelation(2607)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 2607`. Sourced verbatim from
// `postgres/src/include/catalog/pg_conversion.h` and
// `pg_conversion_d.h` (Anum_pg_conversion_* 1–8). M0106-0010 step 3ag.
func pgConversionAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},             // oid
		{Name: "conname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},        // name
		{Name: "connamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},    // oid → pg_namespace
		{Name: "conowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},        // oid → pg_authid
		{Name: "conforencoding", TypeOID: 23, Num: 5, Len: 4, NotNull: true},  // int4
		{Name: "contoencoding", TypeOID: 23, Num: 6, Len: 4, NotNull: true},   // int4
		{Name: "conproc", TypeOID: 24, Num: 7, Len: 4, NotNull: true},         // regproc → pg_proc
		{Name: "condefault", TypeOID: 16, Num: 8, Len: 1, NotNull: true},      // bool
	}
}

// pgOpfamilyAttrs defines the 5-column PG18 pg_opfamily schema. PG
// `RelationBuildDesc(2753) → ScanPgRelation(2753)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 2753`. Sourced verbatim from
// `postgres/src/include/catalog/pg_opfamily.h` (PG18). All five
// columns are fixed-width NOT NULL — no CATALOG_VARLEN columns.
// M0106-0010 step 3bm.
func pgOpfamilyAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},           // oid
		{Name: "opfmethod", TypeOID: 26, Num: 2, Len: 4, NotNull: true},     // oid → pg_am
		{Name: "opfname", TypeOID: 19, Num: 3, Len: 64, NotNull: true},      // name
		{Name: "opfnamespace", TypeOID: 26, Num: 4, Len: 4, NotNull: true},  // oid → pg_namespace
		{Name: "opfowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},      // oid → pg_authid
	}
}

// pgPartitionedTableAttrs defines the 8-column PG18 pg_partitioned_table
// schema. PG `RelationBuildDesc(3350) → ScanPgRelation(3350)` looks up
// the `Form_pg_class` row first; without a nailedLocalRels entry no
// row gets seeded and PG FATALs with `could not open relation with OID
// 3350`. Sourced verbatim from
// `postgres/src/include/catalog/pg_partitioned_table.h` (PG18).
// pg_partitioned_table has no `oid` system column — `partrelid` is the
// primary key. M0106-0010 step 3bs.
func pgPartitionedTableAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "partrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},      // oid → pg_class
		{Name: "partstrat", TypeOID: 18, Num: 2, Len: 1, NotNull: true},      // char
		{Name: "partnatts", TypeOID: 21, Num: 3, Len: 2, NotNull: true},      // int2
		{Name: "partdefid", TypeOID: 26, Num: 4, Len: 4, NotNull: true},      // oid → pg_class (BKI_LOOKUP_OPT)
		{Name: "partattrs", TypeOID: 22, Num: 5, Len: -1, NotNull: true},     // int2vector BKI_FORCE_NOT_NULL
		{Name: "partclass", TypeOID: 30, Num: 6, Len: -1, NotNull: true},     // oidvector → pg_opclass BKI_FORCE_NOT_NULL
		{Name: "partcollation", TypeOID: 30, Num: 7, Len: -1, NotNull: true}, // oidvector → pg_collation BKI_FORCE_NOT_NULL
		{Name: "partexprs", TypeOID: 194, Num: 8, Len: -1, NotNull: false},   // pg_node_tree (CATALOG_VARLEN, nullable)
	}
}


// pgPublicationAttrs defines the 10-column PG18 pg_publication schema.
// PG `RelationBuildDesc(6104) → ScanPgRelation(6104)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 6104`. Sourced verbatim from `postgres/src/include/catalog/pg_publication.h`
// (PG18, PublicationRelationId == 6104). M0106-0010 step 3bu.
// All ten columns are fixed-width and NOT NULL; the heap is currently
// empty (no publications are created at initdb time).
func pgPublicationAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},           // oid
		{Name: "pubname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},      // name
		{Name: "pubowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},      // oid → pg_authid
		{Name: "puballtables", TypeOID: 16, Num: 4, Len: 1, NotNull: true},  // bool
		{Name: "pubinsert", TypeOID: 16, Num: 5, Len: 1, NotNull: true},     // bool
		{Name: "pubupdate", TypeOID: 16, Num: 6, Len: 1, NotNull: true},     // bool
		{Name: "pubdelete", TypeOID: 16, Num: 7, Len: 1, NotNull: true},     // bool
		{Name: "pubtruncate", TypeOID: 16, Num: 8, Len: 1, NotNull: true},   // bool
		{Name: "pubviaroot", TypeOID: 16, Num: 9, Len: 1, NotNull: true},    // bool
		{Name: "pubgencols", TypeOID: 18, Num: 10, Len: 1, NotNull: true},   // char
	}
}

// pgPublicationNamespaceAttrs defines the 3-column PG18
// pg_publication_namespace schema. PG `RelationBuildDesc(6237) →
// ScanPgRelation(6237)` looks up the catalog descriptor during standby
// boot once Step 3bw seeded pg_publication_oid_index (6110); without
// this entry, the backend FATALs with `could not open relation with
// OID 6237`. Sourced verbatim from
// `postgres/src/include/catalog/pg_publication_namespace.h` (PG18,
// PublicationNamespaceRelationId == 6237). M0106-0010 step 3bx.
func pgPublicationNamespaceAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},     // oid
		{Name: "pnpubid", TypeOID: 26, Num: 2, Len: 4, NotNull: true}, // oid → pg_publication
		{Name: "pnnspid", TypeOID: 26, Num: 3, Len: 4, NotNull: true}, // oid → pg_namespace
	}
}

// pgPublicationRelAttrs defines the 5-column PG18 pg_publication_rel
// schema. PG `RelationBuildDesc(6106) → ScanPgRelation(6106)` looks
// up the catalog descriptor during standby boot once Step 3bx seeded
// pg_publication_namespace; without this entry the backend FATALs with
// `could not open relation with OID 6106`. Sourced verbatim from
// `postgres/src/include/catalog/pg_publication_rel.h` (PG18,
// PublicationRelRelationId == 6106). 3 fixed-width NOT NULL columns
// (oid, prpubid, prrelid) + 2 CATALOG_VARLEN nullable columns
// (prqual pg_node_tree, prattrs int2vector — neither carries
// BKI_FORCE_NOT_NULL upstream). The heap is unpopulated at bootstrap
// so the varlena encoder is not exercised. M0106-0010 step 3by.
func pgPublicationRelAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},       // oid
		{Name: "prpubid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},   // oid → pg_publication
		{Name: "prrelid", TypeOID: 26, Num: 3, Len: 4, NotNull: true},   // oid → pg_class
		{Name: "prqual", TypeOID: 194, Num: 4, Len: -1, NotNull: false}, // pg_node_tree (nullable)
		{Name: "prattrs", TypeOID: 22, Num: 5, Len: -1, NotNull: false}, // int2vector (nullable)
	}
}

// pgRangeAttrs defines the 7-column PG18 pg_range schema. PG
// `RelationBuildDesc(3541) → ScanPgRelation(3541)` looks up these
// rows during standby startup; without them the backend FATALs with
// `could not open relation with OID 3541`. Sourced verbatim from
// `postgres/src/include/catalog/pg_range.h` (PG18,
// RangeRelationId == 3541). pg_range has no `oid` system column —
// attnums start at 1 = rngtypid. All 7 columns are fixed-width
// (oid 4B, regproc 4B). BKI_LOOKUP_OPT columns are still NOT NULL in
// the table descriptor (value 0 is a sentinel meaning "no canonical /
// no subdiff"). M0106-0010 step 3bz.
func pgRangeAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "rngtypid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},       // oid → pg_type
		{Name: "rngsubtype", TypeOID: 26, Num: 2, Len: 4, NotNull: true},     // oid → pg_type
		{Name: "rngmultitypid", TypeOID: 26, Num: 3, Len: 4, NotNull: true},  // oid → pg_type
		{Name: "rngcollation", TypeOID: 26, Num: 4, Len: 4, NotNull: true},   // oid → pg_collation (LOOKUP_OPT)
		{Name: "rngsubopc", TypeOID: 26, Num: 5, Len: 4, NotNull: true},      // oid → pg_opclass
		{Name: "rngcanonical", TypeOID: 24, Num: 6, Len: 4, NotNull: true},   // regproc → pg_proc (LOOKUP_OPT)
		{Name: "rngsubdiff", TypeOID: 24, Num: 7, Len: 4, NotNull: true},     // regproc → pg_proc (LOOKUP_OPT)
	}
}

// pgSequenceAttrs defines the 8-column PG18 pg_sequence schema. PG
// `RelationBuildDesc(2224) → ScanPgRelation(2224)` looks up these rows
// during standby startup; without them the backend FATALs with
// `could not open relation with OID 2224`. Sourced verbatim from
// `postgres/src/include/catalog/pg_sequence.h` (PG18,
// SequenceRelationId == 2224). pg_sequence has no `oid` system column —
// attnums start at 1 = seqrelid. All 8 columns are fixed-width and
// NOT NULL (oid 4B, int8 8B, bool 1B). M0106-0010 step 3cb.
func pgSequenceAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "seqrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},     // oid → pg_class (BKI_LOOKUP)
		{Name: "seqtypid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},     // oid → pg_type  (BKI_LOOKUP)
		{Name: "seqstart", TypeOID: 20, Num: 3, Len: 8, NotNull: true},     // int8
		{Name: "seqincrement", TypeOID: 20, Num: 4, Len: 8, NotNull: true}, // int8
		{Name: "seqmax", TypeOID: 20, Num: 5, Len: 8, NotNull: true},       // int8
		{Name: "seqmin", TypeOID: 20, Num: 6, Len: 8, NotNull: true},       // int8
		{Name: "seqcache", TypeOID: 20, Num: 7, Len: 8, NotNull: true},     // int8
		{Name: "seqcycle", TypeOID: 16, Num: 8, Len: 1, NotNull: true},     // bool
	}
}


// pgStatisticExtDataAttrs returns the 6-column PG18 schema for
// `pg_statistic_ext_data` (OID 3429,
// `postgres/src/include/catalog/pg_statistic_ext_data.h:31`,
// `StatisticExtDataRelationId == 3429`). Two fixed-width NOT NULL leading
// columns followed by four CATALOG_VARLEN nullable columns. PG18 row-type
// OIDs from `_pg_statistic_ext_data_d.h` and runtime pg_type lookup against
// PostgreSQL 18.3 confirm the type IDs verbatim:
//
//   1 stxoid          oid             (TypeOID 26,    Len 4,  NotNull)
//   2 stxdinherit     bool            (TypeOID 16,    Len 1,  NotNull)
//   3 stxdndistinct   pg_ndistinct    (TypeOID 3361,  Len -1, nullable)
//   4 stxddependencies pg_dependencies(TypeOID 3402,  Len -1, nullable)
//   5 stxdmcv         pg_mcv_list     (TypeOID 5017,  Len -1, nullable)
//   6 stxdexpr        _pg_statistic   (TypeOID 10028, Len -1, nullable)
//
// pg_statistic_ext_data has no `oid` system column — attnums start at 1.
// The `_pg_statistic` (array of pg_statistic rowtype) OID is 10028, in the
// FirstGenbkiObjectId range (10000..11999) and stable across PG18 installs
// because genbki assigns rowtype/array OIDs deterministically.
func pgStatisticExtDataAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "stxoid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},          // oid → pg_statistic_ext (BKI_LOOKUP)
		{Name: "stxdinherit", TypeOID: 16, Num: 2, Len: 1, NotNull: true},     // bool
		{Name: "stxdndistinct", TypeOID: 3361, Num: 3, Len: -1, NotNull: false},   // pg_ndistinct (varlena)
		{Name: "stxddependencies", TypeOID: 3402, Num: 4, Len: -1, NotNull: false}, // pg_dependencies (varlena)
		{Name: "stxdmcv", TypeOID: 5017, Num: 5, Len: -1, NotNull: false},     // pg_mcv_list (varlena)
		{Name: "stxdexpr", TypeOID: 10028, Num: 6, Len: -1, NotNull: false},   // _pg_statistic array (varlena)
	}
}

// pgStatisticExtAttrs returns the 9-column PG18 schema for `pg_statistic_ext`
// (OID 3381, `postgres/src/include/catalog/pg_statistic_ext.h:33`,
// `StatisticExtRelationId == 3381`). Five fixed-width NOT NULL leading
// columns followed by one CATALOG_VARLEN NOT NULL (stxkeys), one fixed-width
// nullable (stxstattarget), and two CATALOG_VARLEN columns (stxkind NOT
// NULL, stxexprs nullable). Schema verified verbatim against
// `pg_statistic_ext_d.h:28..38` (Anum_pg_statistic_ext_* 1–9, Natts == 9):
//
//	1 oid           oid          (TypeOID 26,   Len  4,  NotNull)
//	2 stxrelid      oid          (TypeOID 26,   Len  4,  NotNull)
//	3 stxname       name         (TypeOID 19,   Len 64,  NotNull)
//	4 stxnamespace  oid          (TypeOID 26,   Len  4,  NotNull)
//	5 stxowner      oid          (TypeOID 26,   Len  4,  NotNull)
//	6 stxkeys       int2vector   (TypeOID 22,   Len -1,  NotNull — BKI_FORCE_NOT_NULL)
//	7 stxstattarget int2         (TypeOID 21,   Len  2,  nullable — BKI_FORCE_NULL)
//	8 stxkind       _char        (TypeOID 1002, Len -1,  NotNull — BKI_FORCE_NOT_NULL)
//	9 stxexprs      pg_node_tree (TypeOID 194,  Len -1,  nullable)
//
// pg_statistic_ext DOES have an `oid` system column (declared in the CATALOG
// block as `Oid oid`), so attnum 1 is `oid`. M0106-0010 step 3cd.
func pgStatisticExtAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},           // oid (system column)
		{Name: "stxrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},      // oid → pg_class (BKI_LOOKUP)
		{Name: "stxname", TypeOID: 19, Num: 3, Len: 64, NotNull: true},      // name
		{Name: "stxnamespace", TypeOID: 26, Num: 4, Len: 4, NotNull: true},  // oid → pg_namespace (BKI_LOOKUP)
		{Name: "stxowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},      // oid → pg_authid (BKI_LOOKUP)
		{Name: "stxkeys", TypeOID: 22, Num: 6, Len: -1, NotNull: true},      // int2vector BKI_FORCE_NOT_NULL
		{Name: "stxstattarget", TypeOID: 21, Num: 7, Len: 2, NotNull: false}, // int2 BKI_FORCE_NULL
		{Name: "stxkind", TypeOID: 1002, Num: 8, Len: -1, NotNull: true},    // _char BKI_FORCE_NOT_NULL
		{Name: "stxexprs", TypeOID: 194, Num: 9, Len: -1, NotNull: false},   // pg_node_tree
	}
}


// pgStatisticAttrs returns the 31-column PG18 schema for pg_statistic.
// Source of truth: `postgres/src/include/catalog/pg_statistic.h` (PG18,
// StatisticRelationId == 2619).
//
// Columns 1..3 are the unique key (starelid, staattnum, stainherit).
// Columns 4..6 are scalar statistics (stanullfrac, stawidth, stadistinct).
// Columns 7..11 are stakindN int2 (NOT NULL — zero when slot unused).
// Columns 12..16 are staopN oid (NOT NULL, BKI_LOOKUP_OPT — zero when unused).
// Columns 17..21 are stacollN oid (NOT NULL, BKI_LOOKUP_OPT — zero when unused).
// Columns 22..26 are stanumbersN _float4 (CATALOG_VARLEN — NULLABLE).
// Columns 27..31 are stavaluesN anyarray (CATALOG_VARLEN — NULLABLE).
//
// pg_statistic has no `oid` system column — attnum 1 is starelid.
// anyarray is type OID 2277; _float4 is type OID 1021; float4 is 700; int4 23.
func pgStatisticAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "starelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},     // oid → pg_class (BKI_LOOKUP)
		{Name: "staattnum", TypeOID: 21, Num: 2, Len: 2, NotNull: true},    // int2
		{Name: "stainherit", TypeOID: 16, Num: 3, Len: 1, NotNull: true},   // bool
		{Name: "stanullfrac", TypeOID: 700, Num: 4, Len: 4, NotNull: true}, // float4
		{Name: "stawidth", TypeOID: 23, Num: 5, Len: 4, NotNull: true},     // int4
		{Name: "stadistinct", TypeOID: 700, Num: 6, Len: 4, NotNull: true}, // float4
		{Name: "stakind1", TypeOID: 21, Num: 7, Len: 2, NotNull: true},     // int2
		{Name: "stakind2", TypeOID: 21, Num: 8, Len: 2, NotNull: true},     // int2
		{Name: "stakind3", TypeOID: 21, Num: 9, Len: 2, NotNull: true},     // int2
		{Name: "stakind4", TypeOID: 21, Num: 10, Len: 2, NotNull: true},    // int2
		{Name: "stakind5", TypeOID: 21, Num: 11, Len: 2, NotNull: true},    // int2
		{Name: "staop1", TypeOID: 26, Num: 12, Len: 4, NotNull: true},      // oid → pg_operator (BKI_LOOKUP_OPT)
		{Name: "staop2", TypeOID: 26, Num: 13, Len: 4, NotNull: true},      // oid
		{Name: "staop3", TypeOID: 26, Num: 14, Len: 4, NotNull: true},      // oid
		{Name: "staop4", TypeOID: 26, Num: 15, Len: 4, NotNull: true},      // oid
		{Name: "staop5", TypeOID: 26, Num: 16, Len: 4, NotNull: true},      // oid
		{Name: "stacoll1", TypeOID: 26, Num: 17, Len: 4, NotNull: true},    // oid → pg_collation (BKI_LOOKUP_OPT)
		{Name: "stacoll2", TypeOID: 26, Num: 18, Len: 4, NotNull: true},    // oid
		{Name: "stacoll3", TypeOID: 26, Num: 19, Len: 4, NotNull: true},    // oid
		{Name: "stacoll4", TypeOID: 26, Num: 20, Len: 4, NotNull: true},    // oid
		{Name: "stacoll5", TypeOID: 26, Num: 21, Len: 4, NotNull: true},    // oid
		{Name: "stanumbers1", TypeOID: 1021, Num: 22, Len: -1, NotNull: false}, // _float4 (CATALOG_VARLEN, NULLABLE)
		{Name: "stanumbers2", TypeOID: 1021, Num: 23, Len: -1, NotNull: false},
		{Name: "stanumbers3", TypeOID: 1021, Num: 24, Len: -1, NotNull: false},
		{Name: "stanumbers4", TypeOID: 1021, Num: 25, Len: -1, NotNull: false},
		{Name: "stanumbers5", TypeOID: 1021, Num: 26, Len: -1, NotNull: false},
		{Name: "stavalues1", TypeOID: 2277, Num: 27, Len: -1, NotNull: false}, // anyarray (CATALOG_VARLEN, NULLABLE)
		{Name: "stavalues2", TypeOID: 2277, Num: 28, Len: -1, NotNull: false},
		{Name: "stavalues3", TypeOID: 2277, Num: 29, Len: -1, NotNull: false},
		{Name: "stavalues4", TypeOID: 2277, Num: 30, Len: -1, NotNull: false},
		{Name: "stavalues5", TypeOID: 2277, Num: 31, Len: -1, NotNull: false},
	}
}

// pgDefaultAclAttrs defines the 5-column PG18 pg_default_acl schema.
// PG `RelationBuildDesc(826) → ScanPgRelation(826)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 826`. Sourced verbatim from
// `postgres/src/include/catalog/pg_default_acl.h` and
// `pg_default_acl_d.h` (Anum_pg_default_acl_* 1–5). M0106-0010 step 3ak.
// The trailing `defaclacl` column is `aclitem[]` (TypeOID 1034) which is
// varlena (Len=-1); BKI_FORCE_NOT_NULL in the upstream header keeps
// NotNull=true. The heap is currently empty (no default ACL rows are
// bootstrapped), so the varlena encoder is not exercised at boot time.
func pgDefaultAclAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},              // oid
		{Name: "defaclrole", TypeOID: 26, Num: 2, Len: 4, NotNull: true},       // oid → pg_authid
		{Name: "defaclnamespace", TypeOID: 26, Num: 3, Len: 4, NotNull: true},  // oid → pg_namespace (0 = all)
		{Name: "defaclobjtype", TypeOID: 18, Num: 4, Len: 1, NotNull: true},    // char (DEFACLOBJ_*)
		{Name: "defaclacl", TypeOID: 1034, Num: 5, Len: -1, NotNull: true},     // aclitem[] (BKI_FORCE_NOT_NULL)
	}
}

// pgEnumAttrs defines the 4-column PG18 pg_enum schema. PG
// `RelationBuildDesc(3501) → ScanPgRelation(3501)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 3501`. Sourced verbatim from `postgres/src/include/catalog/pg_enum.h`
// and `pg_enum_d.h` (Anum_pg_enum_* 1–4, Natts_pg_enum=4). M0106-0010
// step 3an. enumsortorder is float4 (TypeOID 700, Len 4); enumlabel is
// NameData (TypeOID 19, Len 64). All four columns are fixed-width and
// NOT NULL; the heap is currently empty (no enum values are
// bootstrapped at initdb time).
func pgEnumAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},            // oid
		{Name: "enumtypid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},      // oid → owning enum type
		{Name: "enumsortorder", TypeOID: 700, Num: 3, Len: 4, NotNull: true}, // float4
		{Name: "enumlabel", TypeOID: 19, Num: 4, Len: 64, NotNull: true},     // name
	}
}

// pgEventTriggerAttrs defines the 7-column PG18 pg_event_trigger schema.
// PG `RelationBuildDesc(3466) → ScanPgRelation(3466)` looks up the
// pg_class row whose attribute count this function pins; without it, PG
// FATALs with `could not open relation with OID 3466`. Sourced verbatim
// from `postgres/src/include/catalog/pg_event_trigger.h` and
// `pg_event_trigger_d.h` (Anum_pg_event_trigger_* 1–7). evttags is in
// the CATALOG_VARLEN block and has no BKI_FORCE_NOT_NULL attribute, so
// it is nullable. M0106-0010 step 3ar.
func pgEventTriggerAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},         // oid
		{Name: "evtname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},    // name
		{Name: "evtevent", TypeOID: 19, Num: 3, Len: 64, NotNull: true},   // name
		{Name: "evtowner", TypeOID: 26, Num: 4, Len: 4, NotNull: true},    // oid → pg_authid
		{Name: "evtfoid", TypeOID: 26, Num: 5, Len: 4, NotNull: true},     // oid → pg_proc
		{Name: "evtenabled", TypeOID: 18, Num: 6, Len: 1, NotNull: true},  // char
		{Name: "evttags", TypeOID: 1009, Num: 7, Len: -1, NotNull: false}, // text[] (nullable)
	}
}

// pgExtensionAttrs defines the 8-column PG18 pg_extension schema. PG
// `RelationBuildDesc(3079) → ScanPgRelation(3079)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row
// gets seeded and PG FATALs with `could not open relation with OID
// 3079`. Sourced verbatim from `postgres/src/include/catalog/pg_extension.h`
// and `pg_extension_d.h` (Anum_pg_extension_* 1–8). M0106-0010 step 3aw.
// The first five columns are fixed-width NOT NULL; extversion is text
// (TypeOID 25) and BKI_FORCE_NOT_NULL; extconfig is oid[] (TypeOID 1028)
// and extcondition is text[] (TypeOID 1009), both nullable. The heap is
// currently empty (no extensions are bootstrapped at initdb time).
func pgExtensionAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},             // oid
		{Name: "extname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},        // name
		{Name: "extowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},        // oid → pg_authid
		{Name: "extnamespace", TypeOID: 26, Num: 4, Len: 4, NotNull: true},    // oid → pg_namespace
		{Name: "extrelocatable", TypeOID: 16, Num: 5, Len: 1, NotNull: true},  // bool
		{Name: "extversion", TypeOID: 25, Num: 6, Len: -1, NotNull: true},     // text (BKI_FORCE_NOT_NULL)
		{Name: "extconfig", TypeOID: 1028, Num: 7, Len: -1, NotNull: false},   // oid[] (nullable)
		{Name: "extcondition", TypeOID: 1009, Num: 8, Len: -1, NotNull: false}, // text[] (nullable)
	}
}

// pgForeignDataWrapperAttrs defines the 7-column PG18
// pg_foreign_data_wrapper schema. PG `RelationBuildDesc(2328) →
// ScanPgRelation(2328)` looks up the `Form_pg_class` row first; without a
// nailedLocalRels entry no row gets seeded and PG FATALs with `could not
// open relation with OID 2328`. Sourced verbatim from
// `postgres/src/include/catalog/pg_foreign_data_wrapper.h` and
// `pg_foreign_data_wrapper_d.h` (Anum_pg_foreign_data_wrapper_* 1–7).
// M0106-0010 step 3bb. The first five columns are fixed-width NOT NULL;
// fdwacl is aclitem[] (TypeOID 1034) and fdwoptions is text[] (TypeOID
// 1009), both nullable per the CATALOG_VARLEN block convention.
func pgForeignDataWrapperAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},             // oid
		{Name: "fdwname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},        // name
		{Name: "fdwowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},        // oid → pg_authid
		{Name: "fdwhandler", TypeOID: 26, Num: 4, Len: 4, NotNull: true},      // oid → pg_proc (opt)
		{Name: "fdwvalidator", TypeOID: 26, Num: 5, Len: 4, NotNull: true},    // oid → pg_proc (opt)
		{Name: "fdwacl", TypeOID: 1034, Num: 6, Len: -1, NotNull: false},      // aclitem[] (nullable)
		{Name: "fdwoptions", TypeOID: 1009, Num: 7, Len: -1, NotNull: false},  // text[] (nullable)
	}
}

// pgForeignServerAttrs defines the 8-column PG18 pg_foreign_server schema.
// PG `RelationBuildDesc(1417) → ScanPgRelation(1417)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row gets
// seeded and PG FATALs with `could not open relation with OID 1417`.
// Sourced verbatim from `postgres/src/include/catalog/pg_foreign_server.h`
// and `pg_foreign_server_d.h` (Anum_pg_foreign_server_* 1–8). M0106-0010
// step 3be. The first four columns are fixed-width NOT NULL; srvtype and
// srvversion are text (TypeOID 25), srvacl is aclitem[] (TypeOID 1034),
// srvoptions is text[] (TypeOID 1009) — all four in the CATALOG_VARLEN
// block with no BKI_FORCE_NOT_NULL, so nullable.
func pgForeignServerAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},             // oid
		{Name: "srvname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},        // name
		{Name: "srvowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},        // oid → pg_authid
		{Name: "srvfdw", TypeOID: 26, Num: 4, Len: 4, NotNull: true},          // oid → pg_foreign_data_wrapper
		{Name: "srvtype", TypeOID: 25, Num: 5, Len: -1, NotNull: false},       // text (nullable)
		{Name: "srvversion", TypeOID: 25, Num: 6, Len: -1, NotNull: false},    // text (nullable)
		{Name: "srvacl", TypeOID: 1034, Num: 7, Len: -1, NotNull: false},      // aclitem[] (nullable)
		{Name: "srvoptions", TypeOID: 1009, Num: 8, Len: -1, NotNull: false},  // text[] (nullable)
	}
}

// pgForeignTableAttrs defines the 3-column PG18 pg_foreign_table schema.
// PG `RelationBuildDesc(3118) → ScanPgRelation(3118)` looks up the
// `Form_pg_class` row first; without a nailedLocalRels entry no row gets
// seeded and PG FATALs with `could not open relation with OID 3118`.
// Sourced verbatim from `postgres/src/include/catalog/pg_foreign_table.h`
// and `pg_foreign_table_d.h` (Anum_pg_foreign_table_* 1–3,
// Natts_pg_foreign_table == 3). M0106-0010 step 3bh. Unlike most catalogs,
// pg_foreign_table has no `oid` system column — `ftrelid` is the primary
// key. The first two columns are fixed-width NOT NULL oids; ftoptions is
// text[] (TypeOID 1009) in the CATALOG_VARLEN block (nullable).
func pgForeignTableAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "ftrelid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},      // oid → pg_class
		{Name: "ftserver", TypeOID: 26, Num: 2, Len: 4, NotNull: true},     // oid → pg_foreign_server
		{Name: "ftoptions", TypeOID: 1009, Num: 3, Len: -1, NotNull: false}, // text[] (nullable)
	}
}

// pgParameterAclAttrs returns the 3-column PG18 pg_parameter_acl schema
// per `postgres/src/include/catalog/pg_parameter_acl.h`. parname is the
// only non-nullable variable-length column (BKI_FORCE_NOT_NULL); paracl
// is `aclitem[]` (typeOID 1034, same as pg_class.relacl) and nullable
// (BKI_DEFAULT(_null_)). Used by the M0106-0010 Step 3bp nailed shared
// relation entry.
func pgParameterAclAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},      // oid
		{Name: "parname", TypeOID: 25, Num: 2, Len: -1, NotNull: true}, // text BKI_FORCE_NOT_NULL
		{Name: "paracl", TypeOID: 1034, Num: 3, Len: -1, NotNull: false}, // aclitem[] BKI_DEFAULT(_null_)
	}
}

// pgReplicationOriginAttrs defines the 2-column PG18 pg_replication_origin
// schema per `postgres/src/include/catalog/pg_replication_origin.h`.
// Shared catalog (BKI_SHARED_RELATION); roname is BKI_FORCE_NOT_NULL.
// roident is typed as `Oid` (4 bytes) even though the upstream comment
// notes the *value* needs to fit into uint16 — the column is allocated
// manually outside the normal Oid pool, but its on-disk storage is the
// usual 4-byte Oid.
func pgReplicationOriginAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "roident", TypeOID: 26, Num: 1, Len: 4, NotNull: true},  // oid (manually allocated)
		{Name: "roname", TypeOID: 25, Num: 2, Len: -1, NotNull: true},  // text BKI_FORCE_NOT_NULL
	}
}


// pgTablespaceAttrs defines the 5-column PG18 pg_tablespace schema per
// `postgres/src/include/catalog/pg_tablespace.h:29-41` and
// `pg_tablespace_d.h` (Anum_pg_tablespace_* 1..5,
// Natts_pg_tablespace == 5). Shared catalog (BKI_SHARED_RELATION).
// Three fixed-width NOT NULL leading columns (oid 26/4, spcname name 19/64,
// spcowner oid 26/4) + two CATALOG_VARLEN nullable columns (spcacl
// aclitem[] 1034/-1 BKI_DEFAULT(_null_), spcoptions text[] 1009/-1
// BKI_DEFAULT(_null_)). Used by M0106-0010 Step 3ch nailed shared
// relation entry.
func pgTablespaceAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},        // oid
		{Name: "spcname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},   // name (NAMEDATALEN=64)
		{Name: "spcowner", TypeOID: 26, Num: 3, Len: 4, NotNull: true},   // oid → pg_authid
		{Name: "spcacl", TypeOID: 1034, Num: 4, Len: -1, NotNull: false}, // aclitem[] BKI_DEFAULT(_null_)
		{Name: "spcoptions", TypeOID: 1009, Num: 5, Len: -1, NotNull: false}, // text[] BKI_DEFAULT(_null_)
	}
}

// pgDbRoleSettingAttrs defines the 3-column PG18 pg_db_role_setting
// schema per `postgres/src/include/catalog/pg_db_role_setting.h:34-43`
// and `pg_db_role_setting_d.h` (Anum_pg_db_role_setting_* 1..3,
// Natts_pg_db_role_setting == 3). Shared catalog (BKI_SHARED_RELATION).
// Two fixed-width leading oid columns (setdatabase BKI_LOOKUP_OPT(pg_database),
// setrole BKI_LOOKUP_OPT(pg_authid)) + one CATALOG_VARLEN nullable text[]
// (setconfig — GUC settings to apply at login). The oid columns are NotNull
// in practice (the index 2965 keys both); setconfig is the only varlena and
// is nullable by absence of BKI_FORCE_NOT_NULL. Used by M0106-0010 Step
// 3cu nailed shared relation entry.
func pgDbRoleSettingAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "setdatabase", TypeOID: 26, Num: 1, Len: 4, NotNull: true},   // oid (0 ⇒ role-specific)
		{Name: "setrole", TypeOID: 26, Num: 2, Len: 4, NotNull: true},       // oid (0 ⇒ database-specific)
		{Name: "setconfig", TypeOID: 1009, Num: 3, Len: -1, NotNull: false}, // text[] (CATALOG_VARLEN, nullable)
	}
}

// Ensure unused imports don't cause compile errors.
var _ = crc32.ChecksumIEEE
var _ = storage.BlockSize
var _ = fmt.Sprintf
var _ = catalog.DefaultDBOid
