package misc

import (
	"strings"
	"testing"
)

// TestMessageLevelGUCs pins log_min_messages / log_error_verbosity (absent
// until a pg_basebackup of a PG cluster carried them in postgresql.auto.conf
// and stopped the goopg standby at startup) and the hidden config_enum_entry
// rows of the message-level enums. Expected values are PG 18.3's SHOW and
// error output for the same SETs.
func TestMessageLevelGUCs(t *testing.T) {
	r := BuildDefaultRegistry()
	for _, c := range []struct{ name, in, want string }{
		{"log_min_messages", "", "warning"},
		{"log_min_messages", "DEBUG", "debug2"}, // hidden row: shown as debug2
		{"log_min_messages", "panic", "panic"},
		{"log_error_verbosity", "", "default"},
		{"log_error_verbosity", "Verbose", "verbose"},
		{"client_min_messages", "debug", "debug2"},
		{"client_min_messages", "info", "info"}, // hidden, but INFO's only row
	} {
		v, ok := r.Get(c.name)
		if !ok {
			t.Fatalf("no variable %q", c.name)
		}
		if c.in == "" {
			if v.BootVal != c.want || v.Context != ContextSuset {
				t.Errorf("%s: boot %q context %v; want %q superuser", c.name, v.BootVal, v.Context, c.want)
			}
			continue
		}
		got, err := v.canonicalize(c.in)
		if err != nil || got != c.want {
			t.Errorf("%s = %q canonicalizes to %q, %v; want %q", c.name, c.in, got, err, c.want)
		}
	}
	v, _ := r.Get("log_min_messages")
	_, err := v.canonicalize("bogus")
	verr, ok := err.(*ValidationError)
	if !ok || verr.Hint != "Available values: debug5, debug4, debug3, debug2, debug1, info, notice, warning, error, log, fatal, panic." {
		t.Errorf("log_min_messages = bogus: %v", err)
	}
	// hidden rows stay out of enumvals / the HINT
	if c, _ := r.Get("client_min_messages"); strings.Contains(strings.Join(c.EnumOptions, ","), "info") {
		t.Errorf("client_min_messages lists the hidden info row: %v", c.EnumOptions)
	}
}
