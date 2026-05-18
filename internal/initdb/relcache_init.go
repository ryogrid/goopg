package initdb

import (
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
	// Also copy to base/5/ for the "postgres" database.
	src := filepath.Join(dataDir, "base", "1", "pg_internal.init")
	dst := filepath.Join(dataDir, "base", "5", "pg_internal.init")
	srcData, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read base/1/pg_internal.init: %w", err)
	}
	if err := os.WriteFile(dst, srcData, 0o600); err != nil {
		return fmt.Errorf("write base/5/pg_internal.init: %w", err)
	}
	// Read-only to prevent PG from overwriting.
	if err := os.Chmod(dst, 0o400); err != nil {
		return fmt.Errorf("chmod base/5/pg_internal.init: %w", err)
	}

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
	{1262, "pg_database", 1248, 'r', 16, true, pgDatabaseAttrs()},
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
	{3592, "pg_shseclabel", 4066, 'r', 6, true, pgShseclabelAttrs()},
	{6100, "pg_subscription", 6101, 'r', 9, true, pgSubscriptionAttrs()},
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
}, []idxSpec{
	{2671, "pg_database_datname_index"},
	{2672, "pg_database_oid_index"},
	{2676, "pg_authid_rolname_index"},
	{2677, "pg_authid_oid_index"},
	// M0106-0010 Step 3z: pg_auth_members_role_member_index. PG18
	// `postgres/src/include/catalog/pg_auth_members.h:49` declares
	// `AuthMemRoleMemIndexId = 2694` as a 3-column composite
	// btree(roleid, member, grantor). Without this entry
	// RelationIdGetRelation(2694) FATALs because no pg_class row
	// gets seeded; the sibling 2695 follows the same pattern.
	{2694, "pg_auth_members_role_member_index"},
	{2695, "pg_auth_members_member_role_index"},
	{3593, "pg_shseclabel_object_index"},
	// M0106-0010 Step 3bq: pg_parameter_acl_parname_index (OID 6246).
	// PG18 `postgres/src/include/catalog/pg_parameter_acl.h:53` declares
	// `ParameterAclParnameIndexId = 6246` as UNIQUE btree(parname text_ops).
	// Without this entry RelationIdGetRelation(6246) FATALs because no
	// pg_class row gets seeded for the index. Companion to OID 6247
	// (pg_parameter_acl_oid_index, UNIQUE PRIMARY) seeded in Step 3br below.
	{6246, "pg_parameter_acl_parname_index"},
	// M0106-0010 Step 3br: pg_parameter_acl_oid_index (OID 6247).
	// PG18 `postgres/src/include/catalog/pg_parameter_acl.h:54` declares
	// `ParameterAclOidIndexId = 6247` as UNIQUE PRIMARY btree(oid oid_ops),
	// backing MAKE_SYSCACHE(PARAMETERACLOID, …). Without this entry
	// RelationIdGetRelation(6247) FATALs even though the Form_pg_index row
	// exists, because no pg_class row gets seeded. Sibling to OID 6246
	// (parname text_ops UNIQUE non-PKEY) from Step 3bq.
	{6247, "pg_parameter_acl_oid_index"},
	// M0106-0010 Step 3ca: pg_replication_origin_roiident_index (OID 6001).
	// PG18 `postgres/src/include/catalog/pg_replication_origin.h:57` declares
	// `ReplicationOriginIdentIndex = 6001` as UNIQUE PRIMARY btree(roident
	// oid_ops), backing MAKE_SYSCACHE(REPLORIGIDENT, …). Without this entry
	// RelationIdGetRelation(6001) FATALs because no pg_class row gets seeded
	// for the index. Sibling to OID 6002 (roname text_ops UNIQUE non-PKEY).
	{6001, "pg_replication_origin_roiident_index"},
	// M0106-0010 Step 3ca: pg_replication_origin_roname_index (OID 6002).
	// PG18 `postgres/src/include/catalog/pg_replication_origin.h:58` declares
	// `ReplicationOriginNameIndex = 6002` as UNIQUE btree(roname text_ops),
	// backing MAKE_SYSCACHE(REPLORIGNAME, …). Companion to OID 6001
	// (pg_replication_origin_roiident_index, UNIQUE PRIMARY).
	{6002, "pg_replication_origin_roname_index"},
	// M0106-0010 Step 3cf: pg_subscription_oid_index (OID 6114).
	// PG18 `postgres/src/include/catalog/pg_subscription.h:103` declares
	// `SubscriptionObjectIndexId = 6114` as UNIQUE PRIMARY btree(oid
	// oid_ops), backing MAKE_SYSCACHE(SUBSCRIPTIONOID, …). PG's
	// load_critical_index pass opens every declared index of a nailed
	// rel; without this entry RelationIdGetRelation(6114) FATALs because
	// no pg_class row gets seeded.
	{6114, "pg_subscription_oid_index"},
	// M0106-0010 Step 3cf: pg_subscription_subname_index (OID 6115).
	// PG18 `postgres/src/include/catalog/pg_subscription.h:104` declares
	// `SubscriptionNameIndexId = 6115` as UNIQUE composite
	// btree(subdbid oid_ops, subname name_ops), backing
	// MAKE_SYSCACHE(SUBSCRIPTIONNAME, …). Surfaced by
	// `TestE2E_FailoverGoopgToPG/async` as the next FATAL after Step 3ce
	// seeded pg_statistic. Companion to OID 6114 (oid PKEY) over the
	// already-nailed pg_subscription heap OID 6100.
	{6115, "pg_subscription_subname_index"},
	// M0106-0010 Step 3ch: pg_tablespace_oid_index (OID 2697).
	// PG18 `postgres/src/include/catalog/pg_tablespace.h:52` declares
	// `TablespaceOidIndexId = 2697` as UNIQUE PRIMARY btree(oid oid_ops),
	// backing MAKE_SYSCACHE(TABLESPACEOID, …). PG's load_critical_index
	// pass opens every declared index of a nailed rel; without this entry
	// RelationIdGetRelation(2697) FATALs because no pg_class row gets
	// seeded for the index. Sibling to OID 2698 (spcname name_ops UNIQUE
	// non-PKEY).
	{2697, "pg_tablespace_oid_index"},
	// M0106-0010 Step 3ch: pg_tablespace_spcname_index (OID 2698).
	// PG18 `postgres/src/include/catalog/pg_tablespace.h:53` declares
	// `TablespaceNameIndexId = 2698` as UNIQUE btree(spcname name_ops)
	// (no MAKE_SYSCACHE — used directly by get_tablespace_oid()).
	// Companion to OID 2697 (oid PKEY) over the pg_tablespace heap OID 1213
	// nailed in this same step.
	{2698, "pg_tablespace_spcname_index"},
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
	{2618, "pg_rewrite", 83, 'r', 7, false, pgRewriteAttrs()},
	{2620, "pg_trigger", 83, 'r', 8, false, pgTriggerAttrs()},
	{2615, "pg_namespace", 83, 'r', 5, false, pgNamespaceAttrs()},
	{2604, "pg_attrdef", 83, 'r', 3, false, pgAttrdefAttrs()},
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
}, []idxSpec{
	{2703, "pg_type_oid_index"},
	{2704, "pg_type_typname_nsp_index"},
	{2658, "pg_attribute_relid_attnam_index"},
	{2659, "pg_attribute_relid_attnum_index"},
	{2662, "pg_class_oid_index"},
	{2663, "pg_class_relname_nsp_index"},
	{2690, "pg_proc_oid_index"},
	{2691, "pg_proc_proname_args_nsp_index"},
	{2678, "pg_index_indrelid_index"},
	{2679, "pg_index_indexrelid_index"},
	{2687, "pg_opclass_oid_index"},
	{2655, "pg_amproc_oid_index"},
	{2693, "pg_rewrite_rel_rulename_index"},
	{2701, "pg_trigger_tgrelid_tgname_index"},
	{2667, "pg_constraint_oid_index"},
	{2688, "pg_operator_oid_index"},
	{2680, "pg_inherits_relid_seqno_index"},
	{2684, "pg_namespace_nspname_index"},
	{2685, "pg_namespace_oid_index"},
	{2654, "pg_amop_opr_fam_index"},
	// M0106-0010 Step 3x: pg_aggregate_fnoid_index. PG18
	// `postgres/src/include/catalog/pg_aggregate.h:113` declares
	// `AggregateFnoidIndexId = 2650`. RelationInitIndexAccessInfo's
	// `relnatts == indnatts` check (relcache.c:1492) requires this
	// entry to flow through flattenRels → pgIndexNattsByOID so the
	// nailed pg_class row's relnatts matches the pg_index row's
	// indnatts == 1.
	{2650, "pg_aggregate_fnoid_index"},
	// M0106-0010 Step 3y: pg_amop_fam_strat_index. PG18
	// `postgres/src/include/catalog/pg_amop.h:90` declares
	// `AccessMethodStrategyIndexId = 2653` as a 4-column composite
	// btree(amopfamily, amoplefttype, amoprighttype, amopstrategy).
	// Without this entry RelationIdGetRelation(2653) FATALs because
	// no pg_class row gets seeded.
	{2653, "pg_amop_fam_strat_index"},
	// M0106-0010 Step 3ab: pg_cast_oid_index. PG18
	// `postgres/src/include/catalog/pg_cast.h:59` declares
	// `CastOidIndexId = 2660` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Without this entry RelationIdGetRelation(2660)
	// FATALs because no pg_class row gets seeded; flattenRels also
	// derives RelNatts=1 via pgIndexNattsByOID, satisfying
	// RelationInitIndexAccessInfo's relnatts/indnatts check.
	{2660, "pg_cast_oid_index"},
	// M0106-0010 Step 3ac: pg_cast_source_target_index. PG18
	// `postgres/src/include/catalog/pg_cast.h:60` declares
	// `CastSourceTargetIndexId = 2661` as UNIQUE (not PRIMARY) on
	// btree(castsource oid_ops, casttarget oid_ops). Without this entry
	// RelationIdGetRelation(2661) FATALs because no pg_class row gets
	// seeded; flattenRels derives RelNatts=2 via pgIndexNattsByOID,
	// satisfying RelationInitIndexAccessInfo's relnatts/indnatts check.
	{2661, "pg_cast_source_target_index"},
	// M0106-0010 Step 3ad: pg_opclass_am_name_nsp_index. PG18
	// `postgres/src/include/catalog/pg_opclass.h:85` declares
	// `OpclassAmNameNspIndexId = 2686` as UNIQUE (not PRIMARY) on
	// btree(opcmethod oid_ops, opcname name_ops, opcnamespace oid_ops).
	// Without this entry RelationIdGetRelation(2686) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{2686, "pg_opclass_am_name_nsp_index"},
	// M0106-0010 Step 3ae: pg_collation_name_enc_nsp_index. PG18
	// `postgres/src/include/catalog/pg_collation.h:62` declares
	// `CollationNameEncNspIndexId = 3164` as UNIQUE (not PRIMARY) on
	// btree(collname name_ops, collencoding int4_ops, collnamespace oid_ops).
	// Without this entry RelationIdGetRelation(3164) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=3 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{3164, "pg_collation_name_enc_nsp_index"},
	// M0106-0010 Step 3af: pg_collation_oid_index. PG18
	// `postgres/src/include/catalog/pg_collation.h:63` declares
	// `CollationOidIndexId = 3085` as UNIQUE PRIMARY on btree(oid oid_ops).
	// Without this entry RelationIdGetRelation(3085) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=1 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{3085, "pg_collation_oid_index"},
	// M0106-0010 Step 3ah: pg_conversion_default_index. PG18
	// `postgres/src/include/catalog/pg_conversion.h:63` declares
	// `ConversionDefaultIndexId = 2668` as UNIQUE (not PRIMARY) on
	// btree(connamespace oid_ops, conforencoding int4_ops,
	//        contoencoding int4_ops, oid oid_ops).
	// Without this entry RelationIdGetRelation(2668) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=4 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{2668, "pg_conversion_default_index"},
	// M0106-0010 Step 3ai: pg_conversion_oid_index. PG18
	// `postgres/src/include/catalog/pg_conversion.h:65` declares
	// `ConversionOidIndexId = 2670` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Companion to 2668 (Step 3ah composite UNIQUE
	// non-PKEY) and 2669 (conname/nsp composite UNIQUE non-PKEY, deferred
	// to Step 3aj). Without this entry RelationIdGetRelation(2670) FATALs
	// because no pg_class row gets seeded; flattenRels derives RelNatts=1
	// via pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{2670, "pg_conversion_oid_index"},
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
	{2669, "pg_conversion_name_nsp_index"},
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
	{827, "pg_default_acl_role_nsp_obj_index"},
	// M0106-0010 Step 3am: pg_default_acl_oid_index. PG18
	// `postgres/src/include/catalog/pg_default_acl.h:55` declares
	// `DefaultAclOidIndexId = 828` as UNIQUE PRIMARY KEY on
	// btree(oid oid_ops). Companion to 827
	// (pg_default_acl_role_nsp_obj_index, UNIQUE non-PKEY, Step 3al).
	// Without this entry RelationIdGetRelation(828) FATALs because no
	// pg_class row gets seeded; flattenRels derives RelNatts=1 via
	// pgIndexNattsByOID, satisfying RelationInitIndexAccessInfo's
	// relnatts/indnatts check (relcache.c:1492).
	{828, "pg_default_acl_oid_index"},
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
	{3502, "pg_enum_oid_index"},
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
	{3503, "pg_enum_typid_label_index"},
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
	{3534, "pg_enum_typid_sortorder_index"},
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
	{3467, "pg_event_trigger_evtname_index"},
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
	{3468, "pg_event_trigger_oid_index"},
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
	{3080, "pg_extension_oid_index"},
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
	{3081, "pg_extension_name_index"},
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
	{548, "pg_foreign_data_wrapper_name_index"},
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
	{112, "pg_foreign_data_wrapper_oid_index"},
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
	{549, "pg_foreign_server_name_index"},
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
	{113, "pg_foreign_server_oid_index"},
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
	{3119, "pg_foreign_table_relid_index"},
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
	{2681, "pg_language_name_index"},
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
	{2682, "pg_language_oid_index"},
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
	{2689, "pg_operator_oprname_l_r_n_index"},
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
	{2754, "pg_opfamily_am_name_nsp_index"},
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
	{2755, "pg_opfamily_oid_index"},
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
		{3351, "pg_partitioned_table_partrelid_index"},
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
		{6111, "pg_publication_pubname_index"},
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
		{6110, "pg_publication_oid_index"},
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
		{6238, "pg_publication_namespace_oid_index"},
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
		{6239, "pg_publication_namespace_pnnspid_pnpubid_index"},
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
		{6112, "pg_publication_rel_oid_index"},
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
		{6113, "pg_publication_rel_prrelid_prpubid_index"},
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
		{6116, "pg_publication_rel_prpubid_index"},
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
		{3542, "pg_range_rngtypid_index"},
		// M0106-0010 Step 3bz: pg_range_rngmultitypid_index. PG18
		// `postgres/src/include/catalog/pg_range.h:61` declares
		// `RangeMultirangeTypidIndexId = 2228` as UNIQUE (NOT primary)
		// on btree(rngmultitypid oid_ops). Heap OID 3541 (pg_range) is
		// the nailed local rel above (Step 3bz). Backs
		// MAKE_SYSCACHE(RANGEMULTIRANGE, pg_range_rngmultitypid_index, 4).
		// Without this entry RelationIdGetRelation(2228) FATALs.
		{2228, "pg_range_rngmultitypid_index"},
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
		{5002, "pg_sequence_seqrelid_index"},
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
		{3433, "pg_statistic_ext_data_stxoid_inh_index"},
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
		{3380, "pg_statistic_ext_oid_index"},
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
		{3997, "pg_statistic_ext_name_index"},
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
		{3379, "pg_statistic_ext_relid_index"},
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
		{2696, "pg_statistic_relid_att_inh_index"},
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
		{6117, "pg_subscription_rel_srrelid_srsubid_index"},
		// M0106-0010 Step 3ci: pg_transform_oid_index. PG18
		// `postgres/src/include/catalog/pg_transform.h:43` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_transform_oid_index, 3574,
		// TransformOidIndexId, pg_transform, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3576 (pg_transform) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TRFOID,
		// pg_transform_oid_index, 16). Without this entry
		// RelationIdGetRelation(3574) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{3574, "pg_transform_oid_index"},
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
		{3575, "pg_transform_type_lang_index"},
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
		{3609, "pg_ts_config_map_index"},
		// M0106-0010 Step 3ck: pg_ts_config_cfgname_index. PG18
		// `postgres/src/include/catalog/pg_ts_config.h:50` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_config_cfgname_index, 3608,
		// TSConfigNameNspIndexId, pg_ts_config, btree(cfgname name_ops,
		// cfgnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3602 (pg_ts_config) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSCONFIGNAMENSP, pg_ts_config_cfgname_index, 2).
		// Without this entry RelationIdGetRelation(3608) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{3608, "pg_ts_config_cfgname_index"},
		// M0106-0010 Step 3ck: pg_ts_config_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_config.h:51` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_config_oid_index, 3712,
		// TSConfigOidIndexId, pg_ts_config, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3602 (pg_ts_config) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSCONFIGOID,
		// pg_ts_config_oid_index, 2). Without this entry
		// RelationIdGetRelation(3712) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{3712, "pg_ts_config_oid_index"},
		// M0106-0010 Step 3cm: pg_ts_dict_dictname_index. PG18
		// `postgres/src/include/catalog/pg_ts_dict.h:56` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_dict_dictname_index, 3604,
		// TSDictionaryNameNspIndexId, pg_ts_dict, btree(dictname name_ops,
		// dictnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3600 (pg_ts_dict) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSDICTNAMENSP, pg_ts_dict_dictname_index, 2).
		// Without this entry RelationIdGetRelation(3604) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{3604, "pg_ts_dict_dictname_index"},
		// M0106-0010 Step 3cm: pg_ts_dict_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_dict.h:57` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_dict_oid_index, 3605,
		// TSDictionaryOidIndexId, pg_ts_dict, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3600 (pg_ts_dict) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSDICTOID,
		// pg_ts_dict_oid_index, 2). Without this entry
		// RelationIdGetRelation(3605) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{3605, "pg_ts_dict_oid_index"},
		// M0106-0010 Step 3cn: pg_ts_parser_prsname_index. PG18
		// `postgres/src/include/catalog/pg_ts_parser.h:56` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_parser_prsname_index, 3606,
		// TSParserNameNspIndexId, pg_ts_parser, btree(prsname name_ops,
		// prsnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3601 (pg_ts_parser) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSPARSERNAMENSP, pg_ts_parser_prsname_index, 2).
		// Without this entry RelationIdGetRelation(3606) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{3606, "pg_ts_parser_prsname_index"},
		// M0106-0010 Step 3cn: pg_ts_parser_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_parser.h:57` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_parser_oid_index, 3607,
		// TSParserOidIndexId, pg_ts_parser, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3601 (pg_ts_parser) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSPARSEROID,
		// pg_ts_parser_oid_index, 2). Without this entry
		// RelationIdGetRelation(3607) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{3607, "pg_ts_parser_oid_index"},
		// M0106-0010 Step 3co: pg_ts_template_tmplname_index. PG18
		// `postgres/src/include/catalog/pg_ts_template.h:48` declares
		// `DECLARE_UNIQUE_INDEX(pg_ts_template_tmplname_index, 3766,
		// TSTemplateNameNspIndexId, pg_ts_template, btree(tmplname name_ops,
		// tmplnamespace oid_ops))`. UNIQUE (NOT PRIMARY) 2-column composite
		// key. Heap OID 3764 (pg_ts_template) is the nailed local rel above.
		// Backs MAKE_SYSCACHE(TSTEMPLATENAMENSP, pg_ts_template_tmplname_index, 2).
		// Without this entry RelationIdGetRelation(3766) FATALs; flattenRels
		// derives RelNatts=2 via pgIndexNattsByOID.
		{3766, "pg_ts_template_tmplname_index"},
		// M0106-0010 Step 3co: pg_ts_template_oid_index. PG18
		// `postgres/src/include/catalog/pg_ts_template.h:49` declares
		// `DECLARE_UNIQUE_INDEX_PKEY(pg_ts_template_oid_index, 3767,
		// TSTemplateOidIndexId, pg_ts_template, btree(oid oid_ops))`. UNIQUE
		// PRIMARY single-column key. Heap OID 3764 (pg_ts_template) is the
		// nailed local rel above. Backs MAKE_SYSCACHE(TSTEMPLATEOID,
		// pg_ts_template_oid_index, 2). Without this entry
		// RelationIdGetRelation(3767) FATALs; flattenRels derives
		// RelNatts=1 via pgIndexNattsByOID.
		{3767, "pg_ts_template_oid_index"},
	})

func indexNailed(oid uint32, name string, natts int16) nailedRel {
	return nailedRel{
		OID: oid, RelName: name, RelKind: 'i',
		RelNatts: natts, RelType: 0,
		Attrs: indexKeyAttrs(natts),
	}
}

type idxSpec struct {
	OID     uint32
	Name    string
}

func flattenRels(heaps []nailedRel, idxs []idxSpec) []nailedRel {
	var out []nailedRel
	out = append(out, heaps...)
	natts := pgIndexNattsByOID()
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
		out = append(out, indexNailed(idx.OID, idx.Name, n))
	}
	return out
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

func writeRelcacheInitFile(dataDir string, shared bool, rels []nailedRel) error {
	var path string
	if shared {
		path = filepath.Join(dataDir, "global", "pg_internal.init")
	} else {
		path = filepath.Join(dataDir, "base", "1", "pg_internal.init")
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}

	// Magic number
	if err := binary.Write(f, binary.LittleEndian, uint32(relCacheInitFileMagic)); err != nil {
		return err
	}

	for _, rel := range rels {
		// 1. RelationData blob
		relData := buildRelationDataBlob(rel)
		if err := binary.Write(f, binary.LittleEndian, uint32(len(relData))); err != nil {
			return err
		}
		if _, err := f.Write(relData); err != nil {
			return err
		}

		// 2. FormData_pg_class blob
		pgClass := buildPgClassBlob(rel)
		if err := binary.Write(f, binary.LittleEndian, uint32(len(pgClass))); err != nil {
			return err
		}
		if _, err := f.Write(pgClass); err != nil {
			return err
		}

		// 3. FormData_pg_attribute for each column
		for _, a := range rel.Attrs {
			attrBlob := buildPgAttributeBlob(a)
			if err := binary.Write(f, binary.LittleEndian, uint32(len(attrBlob))); err != nil {
				return err
			}
			if _, err := f.Write(attrBlob); err != nil {
				return err
			}
		}

		// 4. Access method options length (zero — no options)
		if err := binary.Write(f, binary.LittleEndian, uint32(0)); err != nil {
			return err
		}
	}

	if err := f.Close(); err != nil {
		return err
	}
	// Write as read-only to prevent PG's write_relcache_init_file from
	// overwriting hand-crafted init data when the standby starts up.
	return os.Chmod(path, 0o400)
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

func pgDatabaseAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "datname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "datdba", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "encoding", TypeOID: 23, Num: 4, Len: 4, NotNull: true},
		{Name: "datlocprovider", TypeOID: 18, Num: 5, Len: 1, NotNull: true},
		{Name: "datistemplate", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "datallowconn", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "datconnlimit", TypeOID: 23, Num: 8, Len: 4, NotNull: true},
		{Name: "datfrozenxid", TypeOID: 28, Num: 9, Len: 4, NotNull: true},
		{Name: "datminmxid", TypeOID: 28, Num: 10, Len: 4, NotNull: true},
		{Name: "dattablespace", TypeOID: 26, Num: 11, Len: 4, NotNull: true},
		{Name: "datcollate", TypeOID: 19, Num: 12, Len: 64, NotNull: true},
		{Name: "datctype", TypeOID: 19, Num: 13, Len: 64, NotNull: true},
		{Name: "daticulocale", TypeOID: 25, Num: 14, Len: -1},
		{Name: "datcollversion", TypeOID: 25, Num: 15, Len: -1},
		{Name: "datacl", TypeOID: 1034, Num: 16, Len: -1},
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

func pgShseclabelAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "classoid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "objoid", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "objsubid", TypeOID: 23, Num: 4, Len: 4, NotNull: true},
		{Name: "provider", TypeOID: 25, Num: 5, Len: -1, NotNull: true},
		{Name: "label", TypeOID: 25, Num: 6, Len: -1, NotNull: true},
	}
}

func pgSubscriptionAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "subdbid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "subskiplsn", TypeOID: 3220, Num: 3, Len: 8, NotNull: true},
		{Name: "subname", TypeOID: 19, Num: 4, Len: 64, NotNull: true},
		{Name: "subowner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "subenabled", TypeOID: 16, Num: 6, Len: 1, NotNull: true},
		{Name: "subbinary", TypeOID: 16, Num: 7, Len: 1, NotNull: true},
		{Name: "substream", TypeOID: 16, Num: 8, Len: 1, NotNull: true},
		{Name: "subtwophasestate", TypeOID: 18, Num: 9, Len: 1, NotNull: true},
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

func pgRewriteAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "ev_class", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "ev_type", TypeOID: 18, Num: 3, Len: 1, NotNull: true},
		{Name: "ev_action", TypeOID: 25, Num: 4, Len: -1, NotNull: true},
		{Name: "ev_owner", TypeOID: 26, Num: 5, Len: 4, NotNull: true},
		{Name: "ev_enabled", TypeOID: 18, Num: 6, Len: 1, NotNull: true},
		{Name: "rulename", TypeOID: 19, Num: 7, Len: 64, NotNull: true},
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

func pgAttrdefAttrs() []nailedAttr {
	return []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "adrelid", TypeOID: 26, Num: 2, Len: 4, NotNull: true},
		{Name: "adnum", TypeOID: 21, Num: 3, Len: 2, NotNull: true},
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

// Ensure unused imports don't cause compile errors.
var _ = crc32.ChecksumIEEE
var _ = storage.BlockSize
var _ = fmt.Sprintf
var _ = catalog.DefaultDBOid
