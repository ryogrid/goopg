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

// TestAlterRoleConfigCanonicalName pins the ALTER ROLE / DATABASE ... SET
// text path's config name: downcased unless double-quoted (var_name is a
// ColId), then normalised by AlterSetting's GUCArrayAdd (configArrayItem),
// which also rejects what validate_option_array_item rejects.
func TestAlterRoleConfigCanonicalName(t *testing.T) {
	for in, want := range map[string]string{
		`WORK_MEM = '2MB'`:   "work_mem",
		`"my.X" = 'y'`:       "my.X",
		`My.Y TO 'z'`:        "my.y",
		`datestyle TO 'iso'`: "datestyle",
	} {
		got, _, ok := splitLeadingConfigName(in)
		if !ok || got != want {
			t.Errorf("splitLeadingConfigName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if name, code, _ := configArrayItem("datestyle", "iso", false); code != "" || name != "DateStyle" {
		t.Errorf("configArrayItem(datestyle) = %q, %q", name, code)
	}
	if name, code, _ := configArrayItem("timezone", "", true); code != "" || name != "TimeZone" {
		t.Errorf("configArrayItem(RESET timezone) = %q, %q", name, code)
	}
	for _, c := range []struct{ name, value, code string }{
		{"no_such_guc", "1", "42704"},
		{"shared_buffers", "1GB", "55P02"},
		{"work_mem", "bogus", "22023"},
	} {
		if _, code, msg := configArrayItem(c.name, c.value, false); string(code) != c.code {
			t.Errorf("configArrayItem(%q, %q) = %q %q; want %s", c.name, c.value, code, msg, c.code)
		}
	}
}
