package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPublicRevokedCatalogs pins the catalogs PG's initdb strips PUBLIC's
// SELECT from (pg_authid, pg_largeobject, pg_statistic, ...): they must not
// take the blanket system-relation SELECT grant, whether heap-backed (by OID)
// or virtual (by name). Ordinary catalogs keep it.
func TestPublicRevokedCatalogs(t *testing.T) {
	cases := []struct {
		tbl  *catalog.Table
		want bool
	}{
		{&catalog.Table{OID: 2613, Schema: "pg_catalog", Name: "pg_largeobject"}, true},
		{&catalog.Table{OID: 1260, Schema: "pg_catalog", Name: "pg_authid"}, true},
		{&catalog.Table{Schema: "pg_catalog", Name: "pg_authid"}, true}, // virtual, no OID
		{&catalog.Table{OID: 1259, Schema: "pg_catalog", Name: "pg_class"}, false},
		{&catalog.Table{Schema: "pg_catalog", Name: "pg_roles"}, false},
		{&catalog.Table{OID: 16384, Schema: "public", Name: "pg_authid"}, false}, // a user table of that name
		{nil, false},
	}
	for _, c := range cases {
		if got := isPublicRevokedCatalog(c.tbl); got != c.want {
			t.Errorf("isPublicRevokedCatalog(%+v) = %v, want %v", c.tbl, got, c.want)
		}
	}
}
