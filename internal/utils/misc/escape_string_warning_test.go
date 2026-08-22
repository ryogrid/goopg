package misc

import "testing"

// TestEscapeStringWarningRegistered pins the no-op GUC stub added for
// M0134-0070: goopg does not emit the deprecated-backslash-escape warning at
// all, but `SET escape_string_warning = ...` / `SHOW escape_string_warning`
// must still succeed instead of erroring "unrecognized configuration
// parameter" (strings.sql regress diff). Name/type/default mirror
// postgres/src/backend/utils/misc/guc_tables.c (COMPAT_OPTIONS_PREVIOUS,
// boolean, default true; not GUC_REPORT).
func TestEscapeStringWarningRegistered(t *testing.T) {
	s := NewSessionRegistry(BuildDefaultRegistry())

	v, eff, ok := s.Get("escape_string_warning")
	if !ok {
		t.Fatal("escape_string_warning is not registered")
	}
	if v.Type != TypeBool {
		t.Errorf("Type = %v, want TypeBool", v.Type)
	}
	if v.BootVal != "on" {
		t.Errorf("BootVal = %q, want %q", v.BootVal, "on")
	}
	if eff != "on" {
		t.Errorf("effective = %q, want %q", eff, "on")
	}
	if v.Context != ContextUserset {
		t.Errorf("Context = %v, want ContextUserset", v.Context)
	}

	if err := s.Set("escape_string_warning", "off", false); err != nil {
		t.Fatalf("Set(off): unexpected error: %v", err)
	}
	if _, eff, _ := s.Get("escape_string_warning"); eff != "off" {
		t.Errorf("after Set(off), effective = %q, want %q", eff, "off")
	}
	if err := s.Set("escape_string_warning", "on", false); err != nil {
		t.Fatalf("Set(on): unexpected error: %v", err)
	}
	if _, eff, _ := s.Get("escape_string_warning"); eff != "on" {
		t.Errorf("after Set(on), effective = %q, want %q", eff, "on")
	}
}

// TestEscapeStringWarningAcceptsBooleanSynonyms confirms the generic
// TypeBool synonym parsing (true/false, yes/no, 1/0, t/f) applies to this
// GUC like any other boolean, matching upstream's boolean-GUC handling.
func TestEscapeStringWarningAcceptsBooleanSynonyms(t *testing.T) {
	for _, tc := range []struct{ set, want string }{
		{"on", "on"},
		{"off", "off"},
		{"true", "on"},
		{"TRUE", "on"},
		{"yes", "on"},
		{"1", "on"},
		{"t", "on"},
		{"false", "off"},
		{"no", "off"},
		{"0", "off"},
		{"f", "off"},
	} {
		t.Run(tc.set, func(t *testing.T) {
			s := NewSessionRegistry(BuildDefaultRegistry())
			if err := s.Set("escape_string_warning", tc.set, false); err != nil {
				t.Fatalf("SET = %s: %v", tc.set, err)
			}
			if _, eff, _ := s.Get("escape_string_warning"); eff != tc.want {
				t.Errorf("SET %s -> %q, want %q", tc.set, eff, tc.want)
			}
		})
	}
}
