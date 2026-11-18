package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// wireTypeCatalog answers the user-type lookups wireTypeOID makes. The embedded
// nil interface makes any other catalog call fail the test loudly.
type wireTypeCatalog struct {
	catalog.Catalog
	sawDB []uint32
}

func (c *wireTypeCatalog) note(db []uint32) { c.sawDB = append(c.sawDB, db...) }

func (c *wireTypeCatalog) LookupEnum(name string, db ...uint32) (*catalog.EnumType, bool) {
	c.note(db)
	if name == "mood" {
		return &catalog.EnumType{OID: 16400, ArrayOID: 16401}, true
	}
	return nil, false
}

func (c *wireTypeCatalog) LookupDomain(name string, db ...uint32) (*catalog.Domain, bool) {
	switch name {
	case "posint":
		return &catalog.Domain{OID: 16410, ArrayOID: 16411, Base: catalog.Type{Name: "int4"}}, true
	case "posint2":
		return &catalog.Domain{OID: 16412, ArrayOID: 16413, BaseOID: 16410}, true
	}
	return nil, false
}

func (c *wireTypeCatalog) LookupDomainByOID(oid uint32) (*catalog.Domain, bool) {
	if oid == 16410 {
		return c.LookupDomain("posint")
	}
	return nil, false
}

func (c *wireTypeCatalog) LookupCompositeType(name string, db ...uint32) *catalog.CompositeType {
	if name == "pair" {
		return &catalog.CompositeType{OID: 16420, ArrayOID: 16421}
	}
	return nil
}

func (c *wireTypeCatalog) LookupRangeType(name string, db ...uint32) (*catalog.RangeType, bool) {
	if name == "myrange" {
		return &catalog.RangeType{OID: 16430, ArrayOID: 16431}, true
	}
	return nil, false
}

func (c *wireTypeCatalog) LookupRangeTypeByMultirangeName(name string) (*catalog.RangeType, bool) {
	if name == "mymultirange" {
		return &catalog.RangeType{MultirangeOID: 16432, MultirangeArrayOID: 16433}, true
	}
	return nil, false
}

func (c *wireTypeCatalog) LookupTable(name parser.ObjectName, db ...uint32) (*catalog.Table, bool) {
	if name.Name == "rel" {
		return &catalog.Table{RowTypeOID: 16442}, true
	}
	return nil, false
}

// TestWireTypeOID pins the RowDescription type OID against the values PG 18.3
// sends (printtup.c SendRowDescriptionMessage, pg_type.dat). Before
// M0149-0001h1a3 every type outside a short list was advertised as text.
func TestWireTypeOID(t *testing.T) {
	cat := &wireTypeCatalog{}
	for _, tc := range []struct {
		typ  catalog.Type
		want uint32
	}{
		{catalog.Type{Name: "bytea"}, 17},
		{catalog.Type{Name: "void"}, 2278},
		{catalog.Type{Name: "json"}, 114},
		{catalog.Type{Name: "jsonb"}, 3802},
		{catalog.Type{Name: "xid"}, 28},
		{catalog.Type{Name: "cid"}, 29},
		{catalog.Type{Name: "regclass"}, 2205},
		{catalog.Type{Name: "record"}, 2249},
		{catalog.Type{Name: "int4range"}, 3904},
		{catalog.Type{Name: "BYTEA"}, 17},
		// Types PG resolves away stay text.
		{catalog.Type{Name: "text"}, 25},
		{catalog.Type{Name: ""}, 25},
		{catalog.Type{Name: "unknown"}, 25},
		{catalog.Type{Name: "anyelement"}, 25},
		{catalog.Type{Name: "no_such_type"}, 25},
		// Arrays, in each spelling the planner produces.
		{catalog.Type{Name: "int4", IsArray: true}, 1007},
		{catalog.Type{Name: "int4[]"}, 1007},
		{catalog.Type{Name: "_int4"}, 1007},
		{catalog.Type{Name: "bytea", IsArray: true}, 1001},
		{catalog.Type{Name: "json", IsArray: true}, 199},
		{catalog.Type{Name: "text", IsArray: true}, 1009},
		// User-defined types; a domain reports its base type.
		{catalog.Type{Name: "mood"}, 16400},
		{catalog.Type{Name: "mood", IsArray: true}, 16401},
		{catalog.Type{Name: "posint"}, 23},
		{catalog.Type{Name: "posint2"}, 23},
		{catalog.Type{Name: "posint", IsArray: true}, 16411},
		{catalog.Type{Name: "pair"}, 16420},
		{catalog.Type{Name: "pair", IsArray: true}, 16421},
		{catalog.Type{Name: "myrange"}, 16430},
		{catalog.Type{Name: "mymultirange"}, 16432},
		{catalog.Type{Name: "rel"}, 16442},
		{catalog.Type{Name: "rel", IsArray: true}, 16441},
		// A user type may begin with "any" or "_".
		{catalog.Type{Name: "anything"}, 25},
	} {
		if got := wireTypeOID(cat, 5, tc.typ); got != tc.want {
			t.Errorf("wireTypeOID(%+v) = %d, want %d", tc.typ, got, tc.want)
		}
	}
	for _, db := range cat.sawDB {
		if db != 5 {
			t.Fatalf("user-type lookup used database %d, want the connection's 5", db)
		}
	}
	if len(cat.sawDB) == 0 {
		t.Fatal("the connection's database never reached the user-type lookup")
	}
	if got := wireTypeOID(nil, 0, catalog.Type{Name: "bytea"}); got != 17 {
		t.Errorf("without a catalog bytea = %d, want 17", got)
	}
}
