package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCoerceCommonTypeArgs checks select_common_type's walk (parse_coerce.c)
// over typed column operands: the preferred type of a category holds, an
// implicit one-way conversion moves the type, categories may not mix, and
// unmodelled types leave the call alone. M0146-0126.
func TestCoerceCommonTypeArgs(t *testing.T) {
	col := func(typ string) Expr {
		return &ColumnRef{Name: "c", Type: catalog.Type{Name: typ}}
	}
	for _, c := range []struct {
		types []string
		want  string // "" = unchanged, "!" = error
	}{
		{[]string{"int4", "numeric"}, "numeric"},
		{[]string{"numeric", "int4"}, "numeric"},
		{[]string{"float8", "numeric"}, "float8"},
		{[]string{"int8", "float4"}, "float4"},
		{[]string{"integer", "bigint"}, "int8"},
		{[]string{"varchar", "text"}, "varchar"},
		{[]string{"text", "varchar"}, "text"},
		{[]string{"date", "timestamptz"}, "timestamptz"},
		{[]string{"timestamp", "date"}, "timestamp"},
		{[]string{"int4", "text"}, "!"},
		{[]string{"uuid", "uuid"}, ""},
		{[]string{"int4", "uuid"}, ""},
	} {
		fc := &FuncCall{Name: "coalesce"}
		for _, typ := range c.types {
			fc.Args = append(fc.Args, col(typ))
		}
		err := coerceCommonTypeArgs(fc)
		switch c.want {
		case "!":
			if err == nil {
				t.Errorf("%v: want an error", c.types)
			}
			continue
		case "":
			if err != nil {
				t.Errorf("%v: err %v", c.types, err)
			}
			for _, a := range fc.Args {
				if _, cast := a.(*CastExpr); cast {
					t.Errorf("%v: unmodelled types were coerced", c.types)
				}
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: err %v", c.types, err)
			continue
		}
		for i, a := range fc.Args {
			got := commonTypeCanonical(exprType(a).Name)
			if got != c.want {
				t.Errorf("%v: arg %d typed %s, want %s", c.types, i, got, c.want)
			}
		}
	}
}
