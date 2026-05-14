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

// TestOnChangeCallbackFires (M0054-0006e-followup) confirms the
// registry-level OnChange callback fires when a session SETs the
// named variable, and on Reset is invoked with the global default.
func TestOnChangeCallbackFires(t *testing.T) {
	r := BuildDefaultRegistry()
	var values []string
	r.OnChange("enable_nestloop_index", func(value string) {
		values = append(values, value)
	})
	sess := NewSessionRegistry(r)
	if err := sess.Set("enable_nestloop_index", "off", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("enable_nestloop_index", "on", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Reset("enable_nestloop_index"); err != nil {
		t.Fatal(err)
	}
	// Expect: ["off", "on", "on"] — Reset returns to the default
	// "on" registered in defaults.go.
	want := []string{"off", "on", "on"}
	if len(values) != len(want) {
		t.Fatalf("got %d callbacks (%v), want %d (%v)", len(values), values, len(want), want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Errorf("callback %d: got %q, want %q", i, values[i], want[i])
		}
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

// TestCheckpointGUCDefaults pins the M0002 GUCs added to the
// default registry. Names, units, ranges, and defaults must match
// upstream's postgres/src/backend/utils/misc/guc_tables.c.
func TestCheckpointGUCDefaults(t *testing.T) {
	r := BuildDefaultRegistry()

	cases := []struct {
		name    string
		bootVal string
	}{
		{"checkpoint_timeout", "300"},
		{"checkpoint_completion_target", "0.9"},
		{"max_wal_size", "1024"},
		{"min_wal_size", "80"},
		{"full_page_writes", "on"},
	}
	for _, c := range cases {
		v, ok := r.Get(c.name)
		if !ok {
			t.Errorf("%s: not registered", c.name)
			continue
		}
		if v.BootVal != c.bootVal {
			t.Errorf("%s: BootVal=%q want %q", c.name, v.BootVal, c.bootVal)
		}
		if v.Display() != c.bootVal {
			t.Errorf("%s: Display=%q want %q", c.name, v.Display(), c.bootVal)
		}
	}

	// Range gates: max_wal_size must reject < 2 (upstream MIN of 2 MB).
	if err := r.Set("max_wal_size", "1MB", SourceConfigFile); err == nil {
		t.Error("expected max_wal_size=1MB to be rejected (< 2 MB)")
	}
	// checkpoint_completion_target must reject > 1.0.
	if err := r.Set("checkpoint_completion_target", "1.5", SourceConfigFile); err == nil {
		t.Error("expected checkpoint_completion_target=1.5 to be rejected (> 1.0)")
	}
	// checkpoint_timeout accepts a unit suffix and converts to seconds.
	if err := r.Set("checkpoint_timeout", "5min", SourceConfigFile); err != nil {
		t.Fatalf("checkpoint_timeout=5min: %v", err)
	}
	if v, _ := r.Get("checkpoint_timeout"); v.Display() != "300" {
		t.Errorf("checkpoint_timeout after 5min set: Display=%q want 300", v.Display())
	}
}


// TestPredicateLockGUCDefaults pins the M0104-0003 SSI predicate-lock
// sizing GUCs. Names, default boot values, and ranges follow
// `postgres/src/backend/utils/misc/guc_tables.c` so existing tooling
// (postgresql.conf templates, parameter probes, pgbench setups) keeps
// behaving the same against goopg as against upstream.
//
// The `-2` default for `max_predicate_locks_per_relation` is the
// upstream shorthand "use per_xact / 2 as the per-relation
// threshold"; the GUC layer surfaces the negative value verbatim and
// the server-side bridge into `mvcc.Manager.SetPredicateLockLimits`
// is the only place that resolves it into positives. A regression
// that rejected `-2` here would break parity with every
// postgresql.conf in the wild.
func TestPredicateLockGUCDefaults(t *testing.T) {
	r := BuildDefaultRegistry()

	cases := []struct {
		name    string
		bootVal string
	}{
		{"max_predicate_locks_per_xact", "64"},
		{"max_predicate_locks_per_relation", "-2"},
		{"max_predicate_locks_per_page", "2"},
	}
	for _, c := range cases {
		v, ok := r.Get(c.name)
		if !ok {
			t.Errorf("%s: not registered", c.name)
			continue
		}
		if v.BootVal != c.bootVal {
			t.Errorf("%s: BootVal=%q want %q", c.name, v.BootVal, c.bootVal)
		}
		if v.Display() != c.bootVal {
			t.Errorf("%s: Display=%q want %q", c.name, v.Display(), c.bootVal)
		}
	}

	// max_predicate_locks_per_xact rejects values below the upstream
	// floor of 10 — a single predicate-lock entry per xact is too few
	// to make coarsening meaningful.
	if err := r.Set("max_predicate_locks_per_xact", "5", SourceConfigFile); err == nil {
		t.Error("expected max_predicate_locks_per_xact=5 to be rejected (< 10)")
	}
	// max_predicate_locks_per_relation accepts the negative shorthand.
	if err := r.Set("max_predicate_locks_per_relation", "-4", SourceConfigFile); err != nil {
		t.Errorf("max_predicate_locks_per_relation=-4: %v", err)
	}
	// max_predicate_locks_per_page rejects negatives — the page-level
	// threshold has no negative-shorthand semantic upstream.
	if err := r.Set("max_predicate_locks_per_page", "-1", SourceConfigFile); err == nil {
		t.Error("expected max_predicate_locks_per_page=-1 to be rejected (must be >= 0)")
	}
}
