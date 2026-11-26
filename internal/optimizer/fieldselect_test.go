package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCompositeFieldTypeStripsSchema pins compositeFieldType's schema
// stripping (M0149-0046j): a composite field declared with a
// schema-qualified type is stored as the parser's token string
// "public . tst_comp_basic_t", and the nested selection ((a).a).a looked
// that whole string up as a type name — 42809 "… which is not a composite
// type" on subscription/t/002_types.pl's tst_comp_mix_array.
func TestCompositeFieldTypeStripsSchema(t *testing.T) {
	for in, want := range map[string]catalog.Type{
		"public . tst_comp_basic_t":     {Name: "tst_comp_basic_t"},
		"public . tst_comp_basic_t [ ]": {Name: "tst_comp_basic_t", IsArray: true},
		"double precision":              {Name: "double precision"},
		"numeric ( 10 , 2 )":            {Name: "numeric"},
		"pg_catalog . int4":             {Name: "int4"},
	} {
		if got := compositeFieldType(in); got.Name != want.Name || got.IsArray != want.IsArray {
			t.Errorf("compositeFieldType(%q) = %+v, want %+v", in, got, want)
		}
	}
}
