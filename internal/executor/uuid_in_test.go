package executor

import "testing"

// TestParseUUIDTextPG pins string_to_uuid's accepted forms (braces,
// hyphens after any odd byte index, upper case) and its rejections, and
// uuid_out's canonical rendering. Regress uuid.sql exercises the same set.
func TestParseUUIDTextPG(t *testing.T) {
	const canon = "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
	ok := []string{
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}",
		"a0eebc999c0b4ef8bb6d6bb9bd380a11",
		"a0ee-bc99-9c0b-4ef8-bb6d-6bb9-bd38-0a11",
		"{a0eebc99-9c0b4ef8-bb6d6bb9-bd380a11}",
	}
	for _, in := range ok {
		b, valid := parseUUIDText(in)
		if !valid || formatUUID(b) != canon {
			t.Errorf("parseUUIDText(%q) = %s, %v", in, formatUUID(b), valid)
		}
	}
	bad := []string{"x", "", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a1", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11-",
		"{a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11}",
		"a0e-ebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "11111111-1111-1111-1111-111111111111123", "g0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"a0eebc99--9c0b-4ef8-bb6d-6bb9bd380a11"}
	for _, in := range bad {
		if _, valid := parseUUIDText(in); valid {
			t.Errorf("parseUUIDText(%q) accepted", in)
		}
	}
}
