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
}, []idxSpec{
	{2671, "pg_database_datname_index"},
	{2672, "pg_database_oid_index"},
	{2676, "pg_authid_rolname_index"},
	{2677, "pg_authid_oid_index"},
	{2695, "pg_auth_members_member_role_index"},
	{3593, "pg_shseclabel_object_index"},
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

// Ensure unused imports don't cause compile errors.
var _ = crc32.ChecksumIEEE
var _ = storage.BlockSize
var _ = fmt.Sprintf
var _ = catalog.DefaultDBOid
