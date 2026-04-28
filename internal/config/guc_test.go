package config

import (
	"strings"
	"testing"
)

func TestVariableCanonicalization(t *testing.T) {
	v := NewVariable(Variable{Name: "x", Type: TypeBool, BootVal: "off", Context: ContextUserset})
	for _, in := range []string{"on", "true", "yes", "1"} {
		if err := v.Set(in, SourceSession); err != nil {
			t.Errorf("Set(%q): %v", in, err)
		}
		if v.Value != "on" {
			t.Errorf("Set(%q) -> %q, want \"on\"", in, v.Value)
		}
	}
	for _, in := range []string{"off", "false", "no", "0"} {
		if err := v.Set(in, SourceSession); err != nil {
			t.Errorf("Set(%q): %v", in, err)
		}
		if v.Value != "off" {
			t.Errorf("Set(%q) -> %q, want \"off\"", in, v.Value)
		}
	}
	if err := v.Set("frobozz", SourceSession); err == nil {
		t.Error("expected error on bad bool value")
	}
}

func TestVariableUnitConversion(t *testing.T) {
	v := NewVariable(Variable{
		Name: "shared_buffers", Type: TypeInt, Unit: UnitKB,
		BootVal: "128", Context: ContextPostmaster,
	})
	// File-driven set bypasses Context gating, exercising
	// setFromFile via Registry.ApplyConfigEntries.
	r := NewRegistry()
	r.MustRegister(v)
	if err := r.ApplyConfigEntries([]ConfigEntry{
		{Name: "shared_buffers", Value: "8MB"},
	}); err != nil {
		t.Fatal(err)
	}
	// 8MB / 1KB = 8192
	if v.Value != "8192" {
		t.Fatalf("got %q, want 8192", v.Value)
	}
}

func TestVariableEnumValidation(t *testing.T) {
	v := NewVariable(Variable{
		Name: "isolation", Type: TypeEnum,
		EnumOptions: []string{"read committed", "repeatable read"},
		BootVal:     "read committed",
		Context:     ContextUserset,
	})
	if err := v.Set("READ COMMITTED", SourceSession); err != nil {
		t.Errorf("case-insensitive enum match: %v", err)
	}
	if err := v.Set("nope", SourceSession); err == nil {
		t.Error("expected error on bad enum value")
	}
}

func TestSetGatesByContext(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	if err := sess.Set("port", "9999", false); err == nil {
		t.Error("expected error: port is Postmaster-context, not SET-able")
	}
	if err := sess.Set("application_name", "X", false); err != nil {
		t.Errorf("application_name SET: %v", err)
	}
	if _, eff, _ := sess.Get("application_name"); eff != "X" {
		t.Errorf("Get application_name = %q, want X", eff)
	}
}

func TestSessionLayering(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	if err := sess.Set("application_name", "session", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("application_name", "local", true); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("application_name"); eff != "local" {
		t.Errorf("local layer = %q, want local", eff)
	}
	sess.EndTransaction()
	if _, eff, _ := sess.Get("application_name"); eff != "session" {
		t.Errorf("after EndTransaction = %q, want session", eff)
	}
	if err := sess.Reset("application_name"); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("application_name"); eff != "" {
		t.Errorf("after Reset = %q, want \"\" (default)", eff)
	}
}

func TestReportableHookFires(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	var got []string
	sess.SetReportableHook(func(name, value string) {
		got = append(got, name+"="+value)
	})
	if err := sess.Set("application_name", "myapp", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("port", "9999", false); err == nil {
		// non-userset; should not fire
		t.Error("expected error on Postmaster-context SET")
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "application_name=") {
		t.Errorf("hook fired %v, want exactly [application_name=myapp]", got)
	}
}

func TestApplyConfigEntriesUnknownReturnsError(t *testing.T) {
	r := BuildDefaultRegistry()
	err := r.ApplyConfigEntries([]ConfigEntry{
		{Name: "nope_such_var", Value: "x", SourceFile: "f", SourceLine: 1},
	})
	if err == nil {
		t.Fatal("expected error for unknown parameter")
	}
}

func TestReportableVariablesSeedPlausibleSet(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	got := sess.ReportableVariables()
	want := map[string]bool{
		"server_version":              false,
		"client_encoding":             false,
		"DateStyle":                   false,
		"TimeZone":                    false,
		"integer_datetimes":           false,
		"standard_conforming_strings": false,
		"application_name":            false,
	}
	for _, kv := range got {
		if _, ok := want[kv.Name]; ok {
			want[kv.Name] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Errorf("expected %q in ReportableVariables", k)
		}
	}
}
