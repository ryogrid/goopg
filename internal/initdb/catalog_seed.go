package initdb

import (
	"github.com/goopg/goopg/internal/catalog"
)

// bootstrapXID is the transaction ID assigned to all system catalog rows
// written during initdb.  It matches mvcc.BootstrapTransactionID (= 1).
// Any new session snapshot starts at Xmin >= 3 (FirstNormalTransactionID),
// so xid=1 is always treated as committed and the rows are visible
// immediately on first startup.
const bootstrapXID = 1

// pgTypeBootstrapRows returns the initial rows for pg_type — one entry
// per built-in type supported by v0.  OID values match upstream's
// postgres/src/include/catalog/pg_type.h.
func pgTypeBootstrapRows() []catalog.PGTypeRow {
	ns := catalog.PGCatalogNamespaceOID
	return []catalog.PGTypeRow{
		{OID: catalog.OIDBool, TypName: "bool", TypNamespace: ns, TypLen: 1, TypByVal: true, TypType: "b", TypCategory: "B"},
		{OID: catalog.OIDInt8, TypName: "int8", TypNamespace: ns, TypLen: 8, TypByVal: true, TypType: "b", TypCategory: "N"},
		{OID: catalog.OIDInt2, TypName: "int2", TypNamespace: ns, TypLen: 2, TypByVal: true, TypType: "b", TypCategory: "N"},
		{OID: catalog.OIDInt4, TypName: "int4", TypNamespace: ns, TypLen: 4, TypByVal: true, TypType: "b", TypCategory: "N"},
		{OID: catalog.OIDText, TypName: "text", TypNamespace: ns, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "S"},
		{OID: catalog.OIDOID, TypName: "oid", TypNamespace: ns, TypLen: 4, TypByVal: true, TypType: "b", TypCategory: "N"},
		{OID: catalog.OIDBpChar, TypName: "bpchar", TypNamespace: ns, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "S"},
		{OID: catalog.OIDVarChar, TypName: "varchar", TypNamespace: ns, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "S"},
		{OID: catalog.OIDTimestamp, TypName: "timestamp", TypNamespace: ns, TypLen: 8, TypByVal: true, TypType: "b", TypCategory: "D"},
		{OID: catalog.OIDNumeric, TypName: "numeric", TypNamespace: ns, TypLen: -1, TypByVal: false, TypType: "b", TypCategory: "N"},
	}
}

// pgClassBootstrapRows returns the initial rows for pg_class — one entry
// per core system catalog.  User tables are added via DDL-sync wiring in
// a later phase.
func pgClassBootstrapRows() []catalog.PGClassRow {
	ns := catalog.PGCatalogNamespaceOID
	return []catalog.PGClassRow{
		// pg_type (OID 1247) — 7 columns
		{
			OID: catalog.TypeRelationId, RelName: "pg_type",
			RelNamespace: ns, RelKind: "r", RelNAtts: 7,
			RelFileNode: catalog.TypeRelationId, RelPersistence: "p",
		},
		// pg_attribute (OID 1249) — 6 columns
		{
			OID: catalog.AttributeRelationId, RelName: "pg_attribute",
			RelNamespace: ns, RelKind: "r", RelNAtts: 6,
			RelFileNode: catalog.AttributeRelationId, RelPersistence: "p",
		},
		// pg_class (OID 1259) — 8 columns
		{
			OID: catalog.RelationRelationId, RelName: "pg_class",
			RelNamespace: ns, RelKind: "r", RelNAtts: 8,
			RelFileNode: catalog.RelationRelationId, RelPersistence: "p",
		},
	}
}

// pgAttributeBootstrapRows returns the initial rows for pg_attribute —
// column definitions for the three core system catalogs.
func pgAttributeBootstrapRows() []catalog.PGAttributeRow {
	var rows []catalog.PGAttributeRow

	// pg_class columns (attrelid = RelationRelationId = 1259).
	pgClassCols := catalog.PGClassColumns()
	for _, col := range pgClassCols {
		typOID := typeOIDForCatalogColumn(col.Type.Name)
		rows = append(rows, catalog.PGAttributeRow{
			AttRelID:  catalog.RelationRelationId,
			AttName:   col.Name,
			AttTypID:  typOID,
			AttNum:    int32(col.Ordinal + 1), // 1-based
			AttNotNull: col.NotNull,
		})
	}

	// pg_attribute columns (attrelid = AttributeRelationId = 1249).
	pgAttrCols := catalog.PGAttributeColumns()
	for _, col := range pgAttrCols {
		typOID := typeOIDForCatalogColumn(col.Type.Name)
		rows = append(rows, catalog.PGAttributeRow{
			AttRelID:  catalog.AttributeRelationId,
			AttName:   col.Name,
			AttTypID:  typOID,
			AttNum:    int32(col.Ordinal + 1),
			AttNotNull: col.NotNull,
		})
	}

	// pg_type columns (attrelid = TypeRelationId = 1247).
	pgTypeCols := catalog.PGTypeColumns()
	for _, col := range pgTypeCols {
		typOID := typeOIDForCatalogColumn(col.Type.Name)
		rows = append(rows, catalog.PGAttributeRow{
			AttRelID:  catalog.TypeRelationId,
			AttName:   col.Name,
			AttTypID:  typOID,
			AttNum:    int32(col.Ordinal + 1),
			AttNotNull: col.NotNull,
		})
	}

	return rows
}

// typeOIDForCatalogColumn maps the v0 type name strings used in the
// PGXxxColumns() schemas to canonical pg_type OIDs.
func typeOIDForCatalogColumn(typName string) uint32 {
	switch typName {
	case "int4", "int", "integer":
		return catalog.OIDInt4
	case "int8", "bigint":
		return catalog.OIDInt8
	case "int2", "smallint":
		return catalog.OIDInt2
	case "bool", "boolean":
		return catalog.OIDBool
	case "text", "varchar", "character varying":
		return catalog.OIDText
	case "oid":
		return catalog.OIDOID
	case "name":
		return 19 // pg_type OID 19 = name
	case "char": // single-byte "char" internal type, OID 18
		return 18
	case "regproc":
		return 24 // OID 24 = regproc
	case "float4", "real":
		return catalog.OIDFloat4
	case "pg_node_tree":
		return 194 // OID 194 = pg_node_tree
	case "aclitem[]", "_aclitem":
		return 1034 // OID 1034 = _aclitem
	default:
		return catalog.OIDText // safe fallback
	}
}
