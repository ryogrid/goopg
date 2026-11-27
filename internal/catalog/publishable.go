package catalog

import (
	"sort"
	"strings"
)

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

// AllTablesPublicationRelations is GetAllTablesPublicationRelations
// (pg_publication.c) over database dbOid: the publishable plain tables,
// partitions included; with publish_via_partition_root, partitions are
// replaced by the partitioned roots (relkind 'p' that are not themselves
// partitions). A partitioned table is never listed otherwise — its rows are
// its partitions'. M0149-0061b.
func AllTablesPublicationRelations(c *InMemory, dbOid uint32, viaRoot bool) []*Table {
	if c == nil {
		return nil
	}
	var out []*Table
	for _, t := range c.AllTables(dbOid) {
		if !IsPublishableTable(t) {
			continue
		}
		partitioned := len(t.PartitionKey) > 0 || t.PartitionMethod != ""
		isPartition := t.PartitionParentOID != 0
		switch {
		case partitioned && !viaRoot:
			continue
		case partitioned && isPartition:
			continue // an intermediate partitioned table
		case !partitioned && isPartition && viaRoot:
			continue
		}
		out = append(out, t)
	}
	return out
}

// PublicationSchemaTables is GetSchemaPublicationRelations with
// PUBLICATION_PART_LEAF (publish_via_partition_root = false, the only mode
// goopg models) for the given schemas of database dbOid: every publishable
// table in them, with a partitioned table replaced by its leaf partitions
// (which may live in other schemas). The result is deduplicated and sorted
// by schema, then name.
func PublicationSchemaTables(c *InMemory, schemas []string, dbOid uint32) []*Table {
	return PublicationSchemaTablesMode(c, schemas, dbOid, false)
}

// PublicationSchemaTablesMode is PublicationSchemaTables in either
// partition mode: viaRoot (PUBLICATION_PART_ROOT, publish_via_partition_root)
// lists a partitioned table itself and leaves out partitions, whose changes
// go out under it. M0149-0057d.
func PublicationSchemaTablesMode(c *InMemory, schemas []string, dbOid uint32, viaRoot bool) []*Table {
	if c == nil || len(schemas) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, s := range schemas {
		want[strings.ToLower(s)] = true
	}
	seen := map[uint32]bool{}
	var out []*Table
	addLeaves := func(t *Table, _ int) {
		if viaRoot {
			if _, isPart := c.PartitionParentOf(t.OID); isPart || seen[t.OID] {
				return
			}
			if len(t.PartitionKey) > 0 || IsPublishableTable(t) {
				seen[t.OID] = true
				out = append(out, t)
			}
			return
		}
		for _, leaf := range PublicationLeafTables(c, t, dbOid) {
			if !seen[leaf.OID] {
				seen[leaf.OID] = true
				out = append(out, leaf)
			}
		}
	}
	for _, t := range c.AllTables(dbOid) {
		if want[strings.ToLower(t.Schema)] {
			addLeaves(t, 0)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PartitionAncestorSchemas returns the schemas of t's partition ancestors,
// nearest first (get_partition_ancestors), for the publication checks that
// publish a partition through an ancestor's schema.
func PartitionAncestorSchemas(c *InMemory, t *Table) []string {
	if c == nil || t == nil {
		return nil
	}
	var out []string
	oid := t.OID
	for depth := 0; depth < 64; depth++ {
		parent, ok := c.PartitionParentOf(oid)
		if !ok {
			break
		}
		pt, _, ok := c.LookupTableByOIDAllDBs(parent)
		if !ok || pt == nil {
			break
		}
		out = append(out, pt.Schema)
		oid = parent
	}
	return out
}

// PublicationLeafTables is GetPubPartitionOptionRelations with
// PUBLICATION_PART_LEAF for one publication member t: a partitioned table
// becomes its publishable leaf partitions (recursively), any other
// publishable table stands for itself. M0149-0054b.
func PublicationLeafTables(c *InMemory, t *Table, dbOid uint32) []*Table {
	var out []*Table
	var walk func(t *Table, depth int)
	walk = func(t *Table, depth int) {
		if t == nil || depth > 64 {
			return
		}
		if len(t.PartitionKey) > 0 && c != nil {
			for _, child := range c.PartitionChildren(t.OID, dbOid) {
				walk(child, depth+1)
			}
			return
		}
		if IsPublishableTable(t) {
			out = append(out, t)
		}
	}
	walk(t, 0)
	return out
}

// PartitionAncestorNames returns the qualified names ("schema.name") of t's
// partition ancestors, nearest first: a FOR TABLE publication of a
// partitioned table publishes its partitions' changes
// (GetTopMostAncestorInPublication). M0149-0054b.
func PartitionAncestorNames(c *InMemory, t *Table) []string {
	if c == nil || t == nil {
		return nil
	}
	var out []string
	oid := t.OID
	for depth := 0; depth < 64; depth++ {
		parent, ok := c.PartitionParentOf(oid)
		if !ok {
			break
		}
		pt, _, ok := c.LookupTableByOIDAllDBs(parent)
		if !ok || pt == nil {
			break
		}
		out = append(out, pt.QualifiedName())
		oid = parent
	}
	return out
}

// PartitionAncestorOIDs returns the OIDs of t's partition ancestors,
// nearest first (get_partition_ancestors). M0149-0057d.
func PartitionAncestorOIDs(c *InMemory, t *Table) []uint32 {
	if c == nil || t == nil {
		return nil
	}
	var out []uint32
	oid := t.OID
	for depth := 0; depth < 64; depth++ {
		parent, ok := c.PartitionParentOf(oid)
		if !ok {
			break
		}
		out = append(out, parent)
		oid = parent
	}
	return out
}
