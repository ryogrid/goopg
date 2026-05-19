package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgOpfamilyEntry mirrors one row of PG18's pg_opfamily. All 5 columns
// are fixed-width NOT NULL, matching FormData_pg_opfamily in pg_opfamily.h.
type pgOpfamilyEntry struct {
	OID       uint32
	Method    uint32
	Name      string
	Namespace uint32
	Owner     uint32
}

// pgOpfamilyColDefs returns the PG18 5-column FormData_pg_opfamily shape.
// The order and types must match pg_opfamily.h exactly.
func pgOpfamilyColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "opfmethod", Type: catalog.Type{Name: "oid"}},
		{Name: "opfname", Type: catalog.Type{Name: "name"}},
		{Name: "opfnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "opfowner", Type: catalog.Type{Name: "oid"}},
	}
}

// pgOpfamilyRow encodes one pg_opfamily row. Field order mirrors
// FormData_pg_opfamily so PG's GETSTRUCT cast is byte-for-byte valid.
func pgOpfamilyRow(e pgOpfamilyEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),       // 1 oid
		executor.NewIntDatum(int64(e.Method)),    // 2 opfmethod
		executor.NewStringDatum(e.Name),          // 3 opfname (NameData)
		executor.NewIntDatum(int64(e.Namespace)), // 4 opfnamespace
		executor.NewIntDatum(int64(e.Owner)),     // 5 opfowner
	}
}

// pgOpfamilyInitialEntries returns all 146 rows of pg_opfamily (OID 2753)
// from PG18's pg_opfamily.dat. OIDs come directly from the .dat file.
// opfnamespace = 11 (pg_catalog), opfowner = 10 (bootstrap superuser).
func pgOpfamilyInitialEntries() []pgOpfamilyEntry {
	const (
		nsPGCatalog uint32 = 11
		ownerSuper  uint32 = 10
		amBtree     uint32 = 403
		amHash      uint32 = 405
		amGist      uint32 = 783
		amGin       uint32 = 2742
		amBrin      uint32 = 4000
		amSpgist    uint32 = 3580
	)
	return []pgOpfamilyEntry{
		{397, amBtree, "array_ops", nsPGCatalog, ownerSuper},          // btree/array_ops
		{627, amHash, "array_ops", nsPGCatalog, ownerSuper},           // hash/array_ops
		{423, amBtree, "bit_ops", nsPGCatalog, ownerSuper},            // btree/bit_ops
		{424, amBtree, "bool_ops", nsPGCatalog, ownerSuper},           // btree/bool_ops
		{426, amBtree, "bpchar_ops", nsPGCatalog, ownerSuper},         // btree/bpchar_ops
		{427, amHash, "bpchar_ops", nsPGCatalog, ownerSuper},          // hash/bpchar_ops
		{428, amBtree, "bytea_ops", nsPGCatalog, ownerSuper},          // btree/bytea_ops
		{429, amBtree, "char_ops", nsPGCatalog, ownerSuper},           // btree/char_ops
		{431, amHash, "char_ops", nsPGCatalog, ownerSuper},            // hash/char_ops
		{434, amBtree, "datetime_ops", nsPGCatalog, ownerSuper},       // btree/datetime_ops
		{435, amHash, "date_ops", nsPGCatalog, ownerSuper},            // hash/date_ops
		{1970, amBtree, "float_ops", nsPGCatalog, ownerSuper},         // btree/float_ops
		{1971, amHash, "float_ops", nsPGCatalog, ownerSuper},          // hash/float_ops
		{1974, amBtree, "network_ops", nsPGCatalog, ownerSuper},       // btree/network_ops
		{1975, amHash, "network_ops", nsPGCatalog, ownerSuper},        // hash/network_ops
		{3550, amGist, "network_ops", nsPGCatalog, ownerSuper},        // gist/network_ops
		{3794, amSpgist, "network_ops", nsPGCatalog, ownerSuper},      // spgist/network_ops
		{1976, amBtree, "integer_ops", nsPGCatalog, ownerSuper},       // btree/integer_ops
		{1977, amHash, "integer_ops", nsPGCatalog, ownerSuper},        // hash/integer_ops
		{1982, amBtree, "interval_ops", nsPGCatalog, ownerSuper},      // btree/interval_ops
		{1983, amHash, "interval_ops", nsPGCatalog, ownerSuper},       // hash/interval_ops
		{1984, amBtree, "macaddr_ops", nsPGCatalog, ownerSuper},       // btree/macaddr_ops
		{1985, amHash, "macaddr_ops", nsPGCatalog, ownerSuper},        // hash/macaddr_ops
		{3371, amBtree, "macaddr8_ops", nsPGCatalog, ownerSuper},      // btree/macaddr8_ops
		{3372, amHash, "macaddr8_ops", nsPGCatalog, ownerSuper},       // hash/macaddr8_ops
		{1988, amBtree, "numeric_ops", nsPGCatalog, ownerSuper},       // btree/numeric_ops
		{1998, amHash, "numeric_ops", nsPGCatalog, ownerSuper},        // hash/numeric_ops
		{1989, amBtree, "oid_ops", nsPGCatalog, ownerSuper},           // btree/oid_ops
		{1990, amHash, "oid_ops", nsPGCatalog, ownerSuper},            // hash/oid_ops
		{1991, amBtree, "oidvector_ops", nsPGCatalog, ownerSuper},     // btree/oidvector_ops
		{1992, amHash, "oidvector_ops", nsPGCatalog, ownerSuper},      // hash/oidvector_ops
		{2994, amBtree, "record_ops", nsPGCatalog, ownerSuper},        // btree/record_ops
		{6194, amHash, "record_ops", nsPGCatalog, ownerSuper},         // hash/record_ops
		{3194, amBtree, "record_image_ops", nsPGCatalog, ownerSuper},  // btree/record_image_ops
		{1994, amBtree, "text_ops", nsPGCatalog, ownerSuper},          // btree/text_ops
		{1995, amHash, "text_ops", nsPGCatalog, ownerSuper},           // hash/text_ops
		{1996, amBtree, "time_ops", nsPGCatalog, ownerSuper},          // btree/time_ops
		{1997, amHash, "time_ops", nsPGCatalog, ownerSuper},           // hash/time_ops
		{1999, amHash, "timestamptz_ops", nsPGCatalog, ownerSuper},    // hash/timestamptz_ops
		{2000, amBtree, "timetz_ops", nsPGCatalog, ownerSuper},        // btree/timetz_ops
		{2001, amHash, "timetz_ops", nsPGCatalog, ownerSuper},         // hash/timetz_ops
		{2002, amBtree, "varbit_ops", nsPGCatalog, ownerSuper},        // btree/varbit_ops
		{2040, amHash, "timestamp_ops", nsPGCatalog, ownerSuper},      // hash/timestamp_ops
		{2095, amBtree, "text_pattern_ops", nsPGCatalog, ownerSuper},  // btree/text_pattern_ops
		{2097, amBtree, "bpchar_pattern_ops", nsPGCatalog, ownerSuper}, // btree/bpchar_pattern_ops
		{2099, amBtree, "money_ops", nsPGCatalog, ownerSuper},          // btree/money_ops
		{2222, amHash, "bool_ops", nsPGCatalog, ownerSuper},           // hash/bool_ops
		{2223, amHash, "bytea_ops", nsPGCatalog, ownerSuper},          // hash/bytea_ops
		{2789, amBtree, "tid_ops", nsPGCatalog, ownerSuper},           // btree/tid_ops
		{2225, amHash, "xid_ops", nsPGCatalog, ownerSuper},            // hash/xid_ops
		{5032, amHash, "xid8_ops", nsPGCatalog, ownerSuper},           // hash/xid8_ops
		{5067, amBtree, "xid8_ops", nsPGCatalog, ownerSuper},          // btree/xid8_ops
		{2226, amHash, "cid_ops", nsPGCatalog, ownerSuper},            // hash/cid_ops
		{2227, amHash, "tid_ops", nsPGCatalog, ownerSuper},            // hash/tid_ops
		{2229, amHash, "text_pattern_ops", nsPGCatalog, ownerSuper},   // hash/text_pattern_ops
		{2231, amHash, "bpchar_pattern_ops", nsPGCatalog, ownerSuper}, // hash/bpchar_pattern_ops
		{2235, amHash, "aclitem_ops", nsPGCatalog, ownerSuper},        // hash/aclitem_ops
		{2593, amGist, "box_ops", nsPGCatalog, ownerSuper},            // gist/box_ops
		{2594, amGist, "poly_ops", nsPGCatalog, ownerSuper},           // gist/poly_ops
		{2595, amGist, "circle_ops", nsPGCatalog, ownerSuper},         // gist/circle_ops
		{1029, amGist, "point_ops", nsPGCatalog, ownerSuper},          // gist/point_ops
		{2745, amGin, "array_ops", nsPGCatalog, ownerSuper},           // gin/array_ops
		{2968, amBtree, "uuid_ops", nsPGCatalog, ownerSuper},          // btree/uuid_ops
		{2969, amHash, "uuid_ops", nsPGCatalog, ownerSuper},           // hash/uuid_ops
		{3253, amBtree, "pg_lsn_ops", nsPGCatalog, ownerSuper},        // btree/pg_lsn_ops
		{3254, amHash, "pg_lsn_ops", nsPGCatalog, ownerSuper},         // hash/pg_lsn_ops
		{3522, amBtree, "enum_ops", nsPGCatalog, ownerSuper},          // btree/enum_ops
		{3523, amHash, "enum_ops", nsPGCatalog, ownerSuper},           // hash/enum_ops
		{3626, amBtree, "tsvector_ops", nsPGCatalog, ownerSuper},      // btree/tsvector_ops
		{3655, amGist, "tsvector_ops", nsPGCatalog, ownerSuper},       // gist/tsvector_ops
		{3659, amGin, "tsvector_ops", nsPGCatalog, ownerSuper},        // gin/tsvector_ops
		{3683, amBtree, "tsquery_ops", nsPGCatalog, ownerSuper},       // btree/tsquery_ops
		{3702, amGist, "tsquery_ops", nsPGCatalog, ownerSuper},        // gist/tsquery_ops
		{3901, amBtree, "range_ops", nsPGCatalog, ownerSuper},         // btree/range_ops
		{3903, amHash, "range_ops", nsPGCatalog, ownerSuper},          // hash/range_ops
		{3919, amGist, "range_ops", nsPGCatalog, ownerSuper},          // gist/range_ops
		{3474, amSpgist, "range_ops", nsPGCatalog, ownerSuper},        // spgist/range_ops
		{4015, amSpgist, "quad_point_ops", nsPGCatalog, ownerSuper},   // spgist/quad_point_ops
		{4016, amSpgist, "kd_point_ops", nsPGCatalog, ownerSuper},     // spgist/kd_point_ops
		{4017, amSpgist, "text_ops", nsPGCatalog, ownerSuper},         // spgist/text_ops
		{4033, amBtree, "jsonb_ops", nsPGCatalog, ownerSuper},         // btree/jsonb_ops
		{4034, amHash, "jsonb_ops", nsPGCatalog, ownerSuper},          // hash/jsonb_ops
		{4036, amGin, "jsonb_ops", nsPGCatalog, ownerSuper},           // gin/jsonb_ops
		{4037, amGin, "jsonb_path_ops", nsPGCatalog, ownerSuper},      // gin/jsonb_path_ops
		{4054, amBrin, "integer_minmax_ops", nsPGCatalog, ownerSuper},          // brin/integer_minmax_ops
		{4602, amBrin, "integer_minmax_multi_ops", nsPGCatalog, ownerSuper},    // brin/integer_minmax_multi_ops
		{4572, amBrin, "integer_bloom_ops", nsPGCatalog, ownerSuper},           // brin/integer_bloom_ops
		{4055, amBrin, "numeric_minmax_ops", nsPGCatalog, ownerSuper},          // brin/numeric_minmax_ops
		{4603, amBrin, "numeric_minmax_multi_ops", nsPGCatalog, ownerSuper},    // brin/numeric_minmax_multi_ops
		{4056, amBrin, "text_minmax_ops", nsPGCatalog, ownerSuper},             // brin/text_minmax_ops
		{4573, amBrin, "text_bloom_ops", nsPGCatalog, ownerSuper},              // brin/text_bloom_ops
		{4574, amBrin, "numeric_bloom_ops", nsPGCatalog, ownerSuper},           // brin/numeric_bloom_ops
		{4058, amBrin, "timetz_minmax_ops", nsPGCatalog, ownerSuper},           // brin/timetz_minmax_ops
		{4604, amBrin, "timetz_minmax_multi_ops", nsPGCatalog, ownerSuper},     // brin/timetz_minmax_multi_ops
		{4575, amBrin, "timetz_bloom_ops", nsPGCatalog, ownerSuper},            // brin/timetz_bloom_ops
		{4059, amBrin, "datetime_minmax_ops", nsPGCatalog, ownerSuper},         // brin/datetime_minmax_ops
		{4605, amBrin, "datetime_minmax_multi_ops", nsPGCatalog, ownerSuper},   // brin/datetime_minmax_multi_ops
		{4576, amBrin, "datetime_bloom_ops", nsPGCatalog, ownerSuper},          // brin/datetime_bloom_ops
		{4062, amBrin, "char_minmax_ops", nsPGCatalog, ownerSuper},             // brin/char_minmax_ops
		{4577, amBrin, "char_bloom_ops", nsPGCatalog, ownerSuper},              // brin/char_bloom_ops
		{4064, amBrin, "bytea_minmax_ops", nsPGCatalog, ownerSuper},            // brin/bytea_minmax_ops
		{4578, amBrin, "bytea_bloom_ops", nsPGCatalog, ownerSuper},             // brin/bytea_bloom_ops
		{4065, amBrin, "name_minmax_ops", nsPGCatalog, ownerSuper},             // brin/name_minmax_ops
		{4579, amBrin, "name_bloom_ops", nsPGCatalog, ownerSuper},              // brin/name_bloom_ops
		{4068, amBrin, "oid_minmax_ops", nsPGCatalog, ownerSuper},              // brin/oid_minmax_ops
		{4606, amBrin, "oid_minmax_multi_ops", nsPGCatalog, ownerSuper},        // brin/oid_minmax_multi_ops
		{4580, amBrin, "oid_bloom_ops", nsPGCatalog, ownerSuper},               // brin/oid_bloom_ops
		{4069, amBrin, "tid_minmax_ops", nsPGCatalog, ownerSuper},              // brin/tid_minmax_ops
		{4581, amBrin, "tid_bloom_ops", nsPGCatalog, ownerSuper},               // brin/tid_bloom_ops
		{4607, amBrin, "tid_minmax_multi_ops", nsPGCatalog, ownerSuper},        // brin/tid_minmax_multi_ops
		{4070, amBrin, "float_minmax_ops", nsPGCatalog, ownerSuper},            // brin/float_minmax_ops
		{4608, amBrin, "float_minmax_multi_ops", nsPGCatalog, ownerSuper},      // brin/float_minmax_multi_ops
		{4582, amBrin, "float_bloom_ops", nsPGCatalog, ownerSuper},             // brin/float_bloom_ops
		{4074, amBrin, "macaddr_minmax_ops", nsPGCatalog, ownerSuper},          // brin/macaddr_minmax_ops
		{4609, amBrin, "macaddr_minmax_multi_ops", nsPGCatalog, ownerSuper},    // brin/macaddr_minmax_multi_ops
		{4583, amBrin, "macaddr_bloom_ops", nsPGCatalog, ownerSuper},           // brin/macaddr_bloom_ops
		{4109, amBrin, "macaddr8_minmax_ops", nsPGCatalog, ownerSuper},         // brin/macaddr8_minmax_ops
		{4610, amBrin, "macaddr8_minmax_multi_ops", nsPGCatalog, ownerSuper},   // brin/macaddr8_minmax_multi_ops
		{4584, amBrin, "macaddr8_bloom_ops", nsPGCatalog, ownerSuper},          // brin/macaddr8_bloom_ops
		{4075, amBrin, "network_minmax_ops", nsPGCatalog, ownerSuper},          // brin/network_minmax_ops
		{4611, amBrin, "network_minmax_multi_ops", nsPGCatalog, ownerSuper},    // brin/network_minmax_multi_ops
		{4102, amBrin, "network_inclusion_ops", nsPGCatalog, ownerSuper},       // brin/network_inclusion_ops
		{4585, amBrin, "network_bloom_ops", nsPGCatalog, ownerSuper},           // brin/network_bloom_ops
		{4076, amBrin, "bpchar_minmax_ops", nsPGCatalog, ownerSuper},           // brin/bpchar_minmax_ops
		{4586, amBrin, "bpchar_bloom_ops", nsPGCatalog, ownerSuper},            // brin/bpchar_bloom_ops
		{4077, amBrin, "time_minmax_ops", nsPGCatalog, ownerSuper},             // brin/time_minmax_ops
		{4612, amBrin, "time_minmax_multi_ops", nsPGCatalog, ownerSuper},       // brin/time_minmax_multi_ops
		{4587, amBrin, "time_bloom_ops", nsPGCatalog, ownerSuper},              // brin/time_bloom_ops
		{4078, amBrin, "interval_minmax_ops", nsPGCatalog, ownerSuper},         // brin/interval_minmax_ops
		{4613, amBrin, "interval_minmax_multi_ops", nsPGCatalog, ownerSuper},   // brin/interval_minmax_multi_ops
		{4588, amBrin, "interval_bloom_ops", nsPGCatalog, ownerSuper},          // brin/interval_bloom_ops
		{4079, amBrin, "bit_minmax_ops", nsPGCatalog, ownerSuper},              // brin/bit_minmax_ops
		{4080, amBrin, "varbit_minmax_ops", nsPGCatalog, ownerSuper},           // brin/varbit_minmax_ops
		{4081, amBrin, "uuid_minmax_ops", nsPGCatalog, ownerSuper},             // brin/uuid_minmax_ops
		{4614, amBrin, "uuid_minmax_multi_ops", nsPGCatalog, ownerSuper},       // brin/uuid_minmax_multi_ops
		{4589, amBrin, "uuid_bloom_ops", nsPGCatalog, ownerSuper},              // brin/uuid_bloom_ops
		{4103, amBrin, "range_inclusion_ops", nsPGCatalog, ownerSuper},         // brin/range_inclusion_ops
		{4082, amBrin, "pg_lsn_minmax_ops", nsPGCatalog, ownerSuper},           // brin/pg_lsn_minmax_ops
		{4615, amBrin, "pg_lsn_minmax_multi_ops", nsPGCatalog, ownerSuper},     // brin/pg_lsn_minmax_multi_ops
		{4590, amBrin, "pg_lsn_bloom_ops", nsPGCatalog, ownerSuper},            // brin/pg_lsn_bloom_ops
		{4104, amBrin, "box_inclusion_ops", nsPGCatalog, ownerSuper},           // brin/box_inclusion_ops
		{5000, amSpgist, "box_ops", nsPGCatalog, ownerSuper},                   // spgist/box_ops
		{5008, amSpgist, "poly_ops", nsPGCatalog, ownerSuper},                  // spgist/poly_ops
		{4199, amBtree, "multirange_ops", nsPGCatalog, ownerSuper},             // btree/multirange_ops
		{4225, amHash, "multirange_ops", nsPGCatalog, ownerSuper},              // hash/multirange_ops
		{6158, amGist, "multirange_ops", nsPGCatalog, ownerSuper},              // gist/multirange_ops
	}
}

// bootstrapPgOpfamilyTuples writes all 146 pg_opfamily rows to
// base/{1,5}/2753 in PG-canonical FormData_pg_opfamily layout.
// Returns TIDs keyed by opfamily OID for index seeding.
func bootstrapPgOpfamilyTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgOpfamilyColDefs()
	entries := pgOpfamilyInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgOpfamilyRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2753", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("bootstrapPgOpfamilyTuples: %w", err)
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}
