package misc

import "testing"

// TestIsCustomGUCName pins the exported wrapper's syntactic rules — mirrors
// guc.c's valid_custom_variable_name (minus the loaded-extension
// reserved-prefix check, which goopg does not track): two or more non-empty
// dot-separated identifiers, each starting with a letter/underscore. Used by
// SET on an unregistered name and by GRANT/REVOKE ... ON PARAMETER's name
// validation (M0119-0004-ACLHEAP).
func TestIsCustomGUCName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"plpgsql.check_asserts", true},
		{"my_ext.some_setting", true},
		{"work_mem", false},   // no separator: not a custom name at all
		{"", false},           // empty
		{".foo", false},       // empty leading component
		{"foo.", false},       // empty trailing component
		{"foo..bar", false},   // empty middle component
		{"1foo.bar", false},   // digit as first char of a component
		{"foo.bar.baz", true}, // more than two components is fine
	}
	for _, c := range cases {
		if got := IsCustomGUCName(c.name); got != c.want {
			t.Errorf("IsCustomGUCName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
