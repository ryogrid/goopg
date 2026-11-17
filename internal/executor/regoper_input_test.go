package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRegoperInputOutput pins regoperin / regoperatorin / regoperout /
// regoperatorout over the pg_operator seed (M0149-0001h1a14): a unique name
// resolves, an overloaded one is 42725, "-" is a name (no InvalidOid
// shorthand), a missing schema is 42883, regoperatorin reports
// parseNameAndArgTypes syntax errors and NONE, and the outputs render the
// bare name / name(lefttype,righttype).
func TestRegoperInputOutput(t *testing.T) {
	ctx := &Context{Catalog: catalog.NewInMemory()}
	code := func(err error) string {
		if ee, ok := err.(*ExecError); ok {
			return ee.Code
		}
		return ""
	}
	d, err := regIdentifierInput(NewStringDatum("||/"), "regoper", ctx, 0)
	if err != nil || d.Kind != KindInt {
		t.Fatalf("||/ = %+v, %v", d, err)
	}
	if got := regoperOut(uint32(d.Int), ctx); got != "||/" {
		t.Fatalf("regoperout(||/) = %q", got)
	}
	if _, err := regIdentifierInput(NewStringDatum("-"), "regoper", ctx, 0); code(err) != "42725" {
		t.Fatalf("- : %v", err)
	}
	if _, err := regIdentifierInput(NewStringDatum("ng_catalog.||/"), "regoper", ctx, 0); code(err) != "42883" {
		t.Fatalf("ng_catalog.||/ : %v", err)
	}
	d, err = regIdentifierInput(NewStringDatum("+(int4,int4)"), "regoperator", ctx, 0)
	if err != nil || d.Int != 551 {
		t.Fatalf("+(int4,int4) = %+v, %v", d, err)
	}
	if got := regoperatorOut(551, ctx); got != "+(integer,integer)" {
		t.Fatalf("regoperatorout(551) = %q", got)
	}
	if got := regoperOut(551, ctx); got != "pg_catalog.+" {
		t.Fatalf("regoperout(551) = %q", got)
	}
	d, err = regIdentifierInput(NewStringDatum("-(NONE,int4)"), "regoperator", ctx, 0)
	if err != nil || d.Kind != KindInt {
		t.Fatalf("-(NONE,int4) = %+v, %v", d, err)
	}
	if got := regoperatorOut(uint32(d.Int), ctx); got != "-(NONE,integer)" {
		t.Fatalf("regoperatorout(prefix -) = %q", got)
	}
	for in, want := range map[string]string{
		"-":                 "22P02", // expected a left parenthesis
		"+(int4,int4":       "22P02", // expected a right parenthesis
		"+(int4,)":          "22P02", // expected a type name
		"+(int4)":           "42P02", // missing argument
		"+(int4,int4,int4)": "54023",
		"++(int4,int4)":     "42883",
	} {
		if _, err := regIdentifierInput(NewStringDatum(in), "regoperator", ctx, 0); code(err) != want {
			t.Errorf("%s: got %v, want %s", in, err, want)
		}
	}
	if regoperOut(0, ctx) != "0" || regoperatorOut(0, ctx) != "0" {
		t.Fatalf("InvalidOid must render as 0")
	}
}
