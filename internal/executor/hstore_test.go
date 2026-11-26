package executor

import (
	"strings"
	"testing"
)

// TestHstoreIn pins hstore_in + hstore_out (contrib/hstore/hstore_io.c,
// M0149-0046f) against PG 18.3 answers: pairs sorted by key length then
// bytes, the first of a duplicate key kept, an unquoted null (any case) is
// NULL while "NULL" quoted is the string, '"' and '\' backslash-escaped on
// output, the empty hstore is "", and the two syntax-error texts.
func TestHstoreIn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`b=>2, a=>1, a=>3, c=>NULL`, `"a"=>"1", "b"=>"2", "c"=>NULL`},
		{`"a"=>"1"`, `"a"=>"1"`},
		{`"yellow horse"=>"moaned"`, `"yellow horse"=>"moaned"`},
		{`"123"=>"321"`, `"123"=>"321"`},
		{``, ``},
		{`a=>null, b=>"NULL"`, `"a"=>NULL, "b"=>"NULL"`},
		{`a=>1,`, `"a"=>"1"`},
		{`zzz=>foo, a=>"x\"y"`, `"a"=>"x\"y", "zzz"=>"foo"`},
		{`k => v`, `"k"=>"v"`},
	} {
		got, err := hstoreIn(tc.in, 0)
		if err != nil {
			t.Fatalf("hstoreIn(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("hstoreIn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{`a=>`, "syntax error in hstore: unexpected end of string"},
		{` a = > 1 `, `syntax error in hstore, near " " at position 4`},
		{`yellow horse=>moaned`, `syntax error in hstore, near "h" at position 7`},
	} {
		_, err := hstoreIn(tc.in, 0)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("hstoreIn(%q) err = %v, want %q", tc.in, err, tc.want)
		}
	}
}
