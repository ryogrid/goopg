package initdb

import "testing"

// TestMappedLocalCatalogPlaceholdersAreNotIndexes guards M0149-0001g slice 6.
// bootstrapMappedLocalCatalogHeaps runs late in initdb and writes an empty HEAP
// page for every OID in mappedLocalCatalogPlaceholderOIDs, overwriting any file
// already there. pg_ts_template_tmplname_index (3766) and
// pg_ts_template_oid_index (3767) were listed, so the btree pages
// bootstrapPostgresDatabase had written were replaced by heap pages and a
// PostgreSQL 18.3 node on the directory failed `_bt_checkpage` ("index … contains
// corrupted page at block 0") on its first VACUUM or text-search-template
// lookup. No index OID may appear in that list.
func TestMappedLocalCatalogPlaceholdersAreNotIndexes(t *testing.T) {
	idx := map[uint32]bool{}
	for _, e := range pgIndexInitialEntries() {
		idx[e.IndexRelid] = true
	}
	for _, oid := range mappedLocalCatalogPlaceholderOIDs() {
		if idx[oid] {
			t.Errorf("OID %d is a bootstrap index but is in mappedLocalCatalogPlaceholderOIDs (its btree would be overwritten with a heap page)", oid)
		}
	}
}
