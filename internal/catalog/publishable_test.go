package catalog

import "testing"

// TestIsPublishableTable pins is_publishable_class (M0149-0041b): a FOR ALL
// TABLES publication covers permanent user tables only — not the system
// catalogs goopg models as tables, not information_schema, and not
// temporary/unlogged tables, views, materialized views, foreign tables,
// sequences or virtual relations.
func TestIsPublishableTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		tbl  Table
		want bool
	}{
		{"user table", Table{Schema: "public", OID: 16400}, true},
		{"pg_type", Table{Schema: "pg_catalog", Name: "pg_type", OID: 1247}, false},
		{"catalog schema with a user OID", Table{Schema: "pg_catalog", OID: 16500}, false},
		{"information_schema", Table{Schema: "information_schema", OID: 13000}, false},
		{"low OID outside catalogs", Table{Schema: "public", OID: 12000}, false},
		{"temporary", Table{Schema: "pg_temp_3", OID: 16401, Temp: true}, false},
		{"unlogged", Table{Schema: "public", OID: 16402, Unlogged: true}, false},
		{"sequence", Table{Schema: "public", OID: 16403, IsSequence: true}, false},
		{"materialized view", Table{Schema: "public", OID: 16404, IsMatView: true}, false},
		{"foreign table", Table{Schema: "public", OID: 16405, ForeignServerName: "srv"}, false},
		{"virtual", Table{Schema: "public", OID: 16406, Virtual: true}, false},
	} {
		if got := IsPublishableTable(&tc.tbl); got != tc.want {
			t.Errorf("%s: IsPublishableTable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
