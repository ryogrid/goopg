package postmaster

import "testing"

// TestFlattenConfigValueList pins what ALTER DATABASE / ROLE ... SET stores
// in pg_db_role_setting: AlterSetting runs ExtractSetVariableArgs, so the
// value is flattened exactly as SET flattens it. Expected strings are PG
// 18.3's setconfig for the same statements.
func TestFlattenConfigValueList(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"search_path", `'Q', public`, `"Q", public`},
		{"search_path", `"$user", public`, `"$user", public`},
		{"search_path", `Foo`, `foo`},
		{"work_mem", `'64MB'`, `64MB`},
		{"DateStyle", `'ISO', 'MDY'`, `ISO, MDY`},
		// not a var_list: the text-level fallback keeps it verbatim
		{"work_mem", `64MB`, `64MB`},
	}
	for _, c := range cases {
		got, ok, err := flattenConfigValueList(c.name, c.in)
		if !ok || err != nil || got != c.want {
			t.Errorf("flattenConfigValueList(%q, %q) = %q, %v, %v; want %q", c.name, c.in, got, ok, err, c.want)
		}
	}
	if _, ok, err := flattenConfigValueList("work_mem", `'1MB', '2MB'`); !ok || err == nil ||
		err.Error() != "SET work_mem takes only one argument" {
		t.Errorf("two values for work_mem: ok=%v err=%v", ok, err)
	}
}

// TestSplitSetRawValue pins the value text the simple-query SET fast path
// hands to the var_list parser: everything after the name and its '=' / TO,
// quotes intact.
func TestSplitSetRawValue(t *testing.T) {
	for body, want := range map[string]string{
		`search_path = 'a b', public`: `'a b', public`,
		`search_path TO "x", y`:       `"x", y`,
		`search_path='a'`:             `'a'`,
		`timezone 'UTC'`:              `'UTC'`,
	} {
		if got := splitSetRawValue(body); got != want {
			t.Errorf("splitSetRawValue(%q) = %q, want %q", body, got, want)
		}
	}
}
