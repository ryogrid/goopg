package misc

import "testing"

// TestObjectDefaultGUCStubs asserts the three object-creation default GUCs
// (default_table_access_method, default_tablespace, default_toast_compression)
// are registered with PG's boot values, contexts, and types. pg_dump/pg_restore
// emit `SET default_tablespace = '';` and `SET default_table_access_method =
// heap;` before every CREATE TABLE section (and `SET default_toast_compression`
// when a column carries non-default compression), so before this stub an
// unregistered name aborted a real-PG dump replay with "unrecognized
// configuration parameter". goopg only implements the heap access method, has no
// real tablespaces, and uses its own built-in TOAST default — pure
// compatibility stubs. Names/defaults mirror
// postgres/src/backend/utils/misc/guc_tables.c (CLIENT_CONN_STATEMENT, all
// PGC_USERSET). M0122-0007.
func TestObjectDefaultGUCStubs(t *testing.T) {
	cases := []struct {
		name    string
		bootVal string
		typ     Type
	}{
		{"default_table_access_method", "heap", TypeString},
		{"default_tablespace", "", TypeString},
		{"default_toast_compression", "pglz", TypeEnum},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())

			v, val, ok := s.Get(tc.name)
			if !ok {
				t.Fatalf("%s not registered", tc.name)
			}
			if val != tc.bootVal {
				t.Errorf("boot value = %q, want %q", val, tc.bootVal)
			}
			if v.Type != tc.typ {
				t.Errorf("type = %v, want %v", v.Type, tc.typ)
			}
			// All three are PGC_USERSET in upstream, so a plain client SET
			// must succeed at the boot value.
			if v.Context != ContextUserset {
				t.Errorf("context = %v, want ContextUserset", v.Context)
			}
			if err := s.Set(tc.name, tc.bootVal, false); err != nil {
				t.Errorf("Set(%s, %q): unexpected error: %v", tc.name, tc.bootVal, err)
			}
		})
	}
}

// TestObjectDefaultGUCValuesAccepted confirms the exact SET forms pg_dump emits
// are accepted, and that default_toast_compression enforces its enum domain
// (pglz|lz4, matching the reference PG 18.3 --with-lz4 build) rather than
// accepting arbitrary strings.
func TestObjectDefaultGUCValuesAccepted(t *testing.T) {
	accept := []struct {
		name string
		val  string
	}{
		{"default_table_access_method", "heap"}, // pg_dump's SET default_table_access_method = heap;
		{"default_tablespace", ""},              // pg_dump's SET default_tablespace = '';
		{"default_tablespace", "pg_default"},    // a named tablespace is accepted-and-ignored
		{"default_toast_compression", "pglz"},
		{"default_toast_compression", "lz4"},
	}
	for _, tc := range accept {
		s := NewSessionRegistry(BuildDefaultRegistry())
		if err := s.Set(tc.name, tc.val, false); err != nil {
			t.Errorf("Set(%s, %q): unexpected error: %v", tc.name, tc.val, err)
		}
	}

	// default_toast_compression is an enum: an out-of-domain value must be
	// rejected exactly as upstream would reject it.
	s := NewSessionRegistry(BuildDefaultRegistry())
	if err := s.Set("default_toast_compression", "zstd", false); err == nil {
		t.Error(`Set(default_toast_compression, "zstd"): expected invalid-enum error, got nil`)
	}
}
