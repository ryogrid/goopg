package misc

import (
	"os"
	"testing"
)

// TestFlattenSetArgs pins flatten_set_variable_args (guc_funcs.c): list
// elements joined with ", ", string elements of a GUC_LIST_QUOTE variable
// identifier-quoted, numbers printed bare, and more than one element
// rejected for a variable without GUC_LIST_INPUT. Expected values are the
// PG 18.3 SHOW output for the same SET statements.
func TestFlattenSetArgs(t *testing.T) {
	str := func(v string) SetArg { return SetArg{Kind: SetArgString, Val: v} }
	cases := []struct {
		name string
		args []SetArg
		want string
	}{
		// SET search_path = 'x''y\z', public
		{"search_path", []SetArg{str(`x'y\z`), str("public")}, `"x'y\z", public`},
		// SET search_path = Foo, "Bar", 'Baz', '$user', public, pg_catalog
		{"search_path", []SetArg{str("foo"), str("Bar"), str("Baz"), str("$user"), str("public"), str("pg_catalog")},
			`foo, "Bar", "Baz", "$user", public, pg_catalog`},
		// reserved words are quoted: SET search_path = true / 'select', "user"
		{"search_path", []SetArg{str("true")}, `"true"`},
		{"search_path", []SetArg{str("a b"), str("select"), str("user")}, `"a b", "select", "user"`},
		// SET search_path = '' and numbers, which are never quoted
		{"search_path", []SetArg{str("")}, `""`},
		{"search_path", []SetArg{{Kind: SetArgInteger, Val: "1"}, {Kind: SetArgFloat, Val: "2.5"}}, `1, 2.5`},
		// GUC_LIST_INPUT without GUC_LIST_QUOTE: joined, not quoted
		{"DateStyle", []SetArg{str("ISO"), str("MDY")}, `ISO, MDY`},
		{"datestyle", []SetArg{str("ISO"), str("MDY")}, `ISO, MDY`},
		// a scalar variable keeps its one element verbatim
		{"application_name", []SetArg{str("A b")}, `A b`},
		{"my.custom", []SetArg{str("A")}, `A`},
	}
	for _, c := range cases {
		got, err := FlattenSetArgs(c.name, c.args)
		if err != nil || got != c.want {
			t.Errorf("FlattenSetArgs(%q, %v) = %q, %v; want %q", c.name, c.args, got, err, c.want)
		}
	}
	for _, name := range []string{"work_mem", "my.custom", "no_such_guc"} {
		_, err := FlattenSetArgs(name, []SetArg{str("a"), str("b")})
		want := "SET " + name + " takes only one argument"
		if err == nil || err.Error() != want {
			t.Errorf("FlattenSetArgs(%q, two args) error = %v; want %q", name, err, want)
		}
	}
}

// TestCanonicalSettingSpellings pins the check hooks that rewrite the stored
// spelling, which matter once SET downcases unquoted identifiers:
// check_client_encoding (pg_encoding_to_char) and check_timezone (pg_tzset
// takes the tz file's own spelling).
func TestCanonicalSettingSpellings(t *testing.T) {
	r := BuildDefaultRegistry()
	_, tzErr := os.Stat("/usr/share/zoneinfo/America/New_York")
	for _, c := range []struct{ name, in, want string }{
		{"client_encoding", "utf8", "UTF8"},
		{"client_encoding", "UTF-8", "UTF8"},
		{"TimeZone", "utc", "UTC"},
		{"TimeZone", "america/new_york", "America/New_York"},
		{"TimeZone", "pst8pdt", "PST8PDT"},
		{"TimeZone", "UTC", "UTC"},
		{"TimeZone", "<+02>-02", "<+02>-02"},
	} {
		if c.name == "TimeZone" && tzErr != nil {
			continue // no system tzdata to match against
		}
		v, ok := r.Get(c.name)
		if !ok {
			t.Fatalf("no variable %q", c.name)
		}
		got, err := v.canonicalize(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s = %q canonicalizes to %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
}
