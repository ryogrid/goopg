package catalog

import "strings"

// FirstNormalObjectId is the first OID assigned to user objects
// (access/transam.h); every OID below it belongs to the system catalogs.
const FirstNormalObjectId = 16384

// IsPublishableTable is is_publishable_class (catalog/pg_publication.c): a
// FOR ALL TABLES publication — and pg_publication_tables /
// pg_get_publication_tables, which a subscriber's table sync reads — covers
// only ordinary and partitioned tables that are permanent, outside the
// system catalogs (IsCatalogRelationOid: pg_catalog, plus the
// information_schema and TOAST relations initdb creates below
// FirstNormalObjectId), and user-created (OID >= FirstNormalObjectId).
// Views, materialized views, foreign tables, sequences and goopg's virtual
// relations are not publishable. M0149-0041b.
func IsPublishableTable(t *Table) bool {
	if t == nil || t.Virtual || t.IsSequence || t.IsMatView || t.View != nil || t.ForeignServerName != "" {
		return false
	}
	if t.Temp || t.Unlogged {
		return false
	}
	switch strings.ToLower(t.Schema) {
	case "pg_catalog", "information_schema", "pg_toast":
		return false
	}
	return t.OID >= FirstNormalObjectId
}
