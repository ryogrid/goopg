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

// TestFormatDisplayValue pins the exact (raw -> display) mappings observed
// against a real, separately-initdb'd PostgreSQL 18.3 instance for SHOW on
// these GUCs (see the M0119-0004-ACLHEAP loop #79 ledger row's deferred item
// (1)): a unit-flagged int GUC's raw canonical (unitless base-unit) value
// must render with the greatest evenly-dividing unit, and 0/negative values
// (the "disabled" sentinel several timeout GUCs use) must render bare, with
// no unit suffix at all — mirrors guc.c's ShowGUCOption(record, use_units=true)
// -> convert_int_from_base_unit, which only converts when result > 0.
func TestFormatDisplayValue(t *testing.T) {
	cases := []struct {
		name string
		unit Unit
		raw  string
		want string
	}{
		{"work_mem", UnitKB, "78848", "77MB"},       // SET work_mem = '77MB'
		{"shared_buffers", UnitKB, "131072", "128MB"}, // 128MB boot value
		{"checkpoint_timeout", UnitS, "300", "5min"},
		{"deadlock_timeout", UnitMs, "1000", "1s"},
		{"wal_receiver_status_interval", UnitS, "10", "10s"},
		{"statement_timeout", UnitMs, "90000", "90s"},
		{"statement_timeout (disabled)", UnitMs, "0", "0"},
		{"lock_timeout (disabled)", UnitMs, "0", "0"},
		{"max_slot_wal_keep_size (disabled)", UnitMB, "-1", "-1"},
		{"min_wal_size", UnitMB, "80", "80MB"},
		{"effective_cache_size", UnitKB, "4194304", "4GB"},
		{"odd kb amount, no evenly-dividing MB", UnitKB, "1025", "1025kB"},
	}
	for _, c := range cases {
		v := NewVariable(Variable{Name: "x", Type: TypeInt, Unit: c.unit, BootVal: c.raw, Context: ContextUserset})
		if got := v.FormatDisplayValue(c.raw); got != c.want {
			t.Errorf("%s: FormatDisplayValue(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
	// Non-unit and non-int GUCs pass through unchanged.
	str := NewVariable(Variable{Name: "y", Type: TypeString, BootVal: "hello", Context: ContextUserset})
	if got := str.FormatDisplayValue("hello"); got != "hello" {
		t.Errorf("string GUC: got %q, want unchanged %q", got, "hello")
	}
	unitless := NewVariable(Variable{Name: "z", Type: TypeInt, BootVal: "5", Context: ContextUserset})
	if got := unitless.FormatDisplayValue("5"); got != "5" {
		t.Errorf("unitless int GUC: got %q, want unchanged %q", got, "5")
	}
}

// TestSessionRegistryGetDisplay confirms GetDisplay/AllDisplay format
// unit-flagged GUCs while Get/All keep returning the raw bare-number form
// internal consumers (e.g. deadlockTimeout) rely on.
func TestSessionRegistryGetDisplay(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(NewVariable(Variable{
		Name: "work_mem", Type: TypeInt, Unit: UnitKB,
		BootVal: "512MB", Context: ContextUserset,
	}))
	sess := NewSessionRegistry(r)
	if err := sess.Set("work_mem", "77MB", false); err != nil {
		t.Fatal(err)
	}
	if _, raw, _ := sess.Get("work_mem"); raw != "78848" {
		t.Errorf("Get raw = %q, want 78848", raw)
	}
	if _, disp, _ := sess.GetDisplay("work_mem"); disp != "77MB" {
		t.Errorf("GetDisplay = %q, want 77MB", disp)
	}
	found := false
	for _, kv := range sess.AllDisplay() {
		if kv.Name == "work_mem" {
			found = true
			if kv.Value != "77MB" {
				t.Errorf("AllDisplay work_mem = %q, want 77MB", kv.Value)
			}
		}
	}
	if !found {
		t.Fatal("AllDisplay did not include work_mem")
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

// TestApplyReloadEntriesAppliesSigHupSkipsPostmaster verifies the
// three-way split ApplyReloadEntries must make on a running server: a
// PGC_SIGHUP variable (checkpoint_timeout) takes effect immediately, a
// PGC_POSTMASTER variable (max_connections) is left untouched with a
// warning (it needs a real restart), and an unknown name is reported
// as a warning too — none of it aborts the whole reload.
func TestApplyReloadEntriesAppliesSigHupSkipsPostmaster(t *testing.T) {
	r := BuildDefaultRegistry()
	origMaxConn, _ := r.Get("max_connections")
	beforeMaxConn := origMaxConn.Value

	result := r.ApplyReloadEntries([]ConfigEntry{
		{Name: "checkpoint_timeout", Value: "600", SourceFile: "postgresql.conf", SourceLine: 1},
		{Name: "max_connections", Value: "200", SourceFile: "postgresql.conf", SourceLine: 2},
		{Name: "nope_such_var", Value: "x", SourceFile: "postgresql.conf", SourceLine: 3},
	})

	if len(result.Changed) != 1 || result.Changed[0] != "checkpoint_timeout" {
		t.Fatalf("Changed = %v, want [checkpoint_timeout]", result.Changed)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("Warnings = %v, want 2 entries (max_connections restart-required + unknown param)", result.Warnings)
	}

	ct, _ := r.Get("checkpoint_timeout")
	if ct.Value != "600" {
		t.Errorf("checkpoint_timeout = %q, want %q (PGC_SIGHUP must apply on reload)", ct.Value, "600")
	}
	if ct.Source != SourceConfigFile {
		t.Errorf("checkpoint_timeout.Source = %v, want SourceConfigFile", ct.Source)
	}

	mc, _ := r.Get("max_connections")
	if mc.Value != beforeMaxConn {
		t.Errorf("max_connections = %q, want unchanged %q (PGC_POSTMASTER must not apply on reload)", mc.Value, beforeMaxConn)
	}
}

// TestApplyReloadEntriesFiresOnChange verifies a reload-driven value
// change bridges to the same OnChange callback a SET does, since
// (unlike boot-time ApplyConfigEntries) the server is already live
// and process-global toggles need to observe the new value.
func TestApplyReloadEntriesFiresOnChange(t *testing.T) {
	r := BuildDefaultRegistry()
	var got string
	r.OnChange("enable_nestloop_index", func(value string) { got = value })

	r.ApplyReloadEntries([]ConfigEntry{
		{Name: "enable_nestloop_index", Value: "off", SourceFile: "postgresql.conf", SourceLine: 1},
	})

	if got != "off" {
		t.Fatalf("OnChange callback got %q, want %q", got, "off")
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

// TestExplainVariablesEmptyByDefault: a freshly built registry has no
// modified GUCs, so EXPLAIN (SETTINGS) has nothing to report — mirrors
// upstream's get_explain_guc_options returning zero entries at server
// startup before any SET.
func TestExplainVariablesEmptyByDefault(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	if got := sess.ExplainVariables(); len(got) != 0 {
		t.Fatalf("ExplainVariables() = %v, want empty", got)
	}
}

// TestExplainVariablesReportsModifiedPlannerGUC: SET enable_seqscan = off
// (a FlagExplain GUC) surfaces in ExplainVariables once it diverges from
// its BootVal; an unmodified FlagExplain GUC (work_mem) and a modified
// non-FlagExplain GUC (client_encoding) must not appear.
func TestExplainVariablesReportsModifiedPlannerGUC(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	if err := sess.Set("enable_seqscan", "off", false); err != nil {
		t.Fatalf("Set enable_seqscan: %v", err)
	}
	if err := sess.Set("client_encoding", "SQL_ASCII", false); err != nil {
		t.Fatalf("Set client_encoding: %v", err)
	}

	got := sess.ExplainVariables()
	var found *ReportableValue
	for i := range got {
		if got[i].Name == "enable_seqscan" {
			found = &got[i]
		}
		if got[i].Name == "client_encoding" {
			t.Errorf("client_encoding (not FlagExplain) leaked into ExplainVariables: %+v", got[i])
		}
		if got[i].Name == "work_mem" {
			t.Errorf("unmodified work_mem leaked into ExplainVariables: %+v", got[i])
		}
	}
	if found == nil || found.Value != "off" {
		t.Fatalf("ExplainVariables() = %v, want enable_seqscan = off", got)
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

// TestClientEncodingValidation verifies SET client_encoding rejects unknown
// encoding names and accepts valid ones. M0122-0008.
func TestClientEncodingValidation(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	// Valid encodings (canonical names).
	for _, enc := range []string{
		"UTF8", "LATIN1", "SQL_ASCII", "EUC_JP", "WIN1252",
		"SJIS", "GBK", "BIG5", // client-only encodings are valid for client_encoding
	} {
		if err := sess.Set("client_encoding", enc, false); err != nil {
			t.Errorf("SET client_encoding = %q: unexpected error: %v", enc, err)
		}
	}

	// Valid encodings (aliases — punctuation-stripped, lowercased).
	for _, enc := range []string{
		"utf8", "utf-8", "utf_8",
		"unicode",
		"latin1", "LATIN-1",
		"sql_ascii", "sql-ascii",
	} {
		if err := sess.Set("client_encoding", enc, false); err != nil {
			t.Errorf("SET client_encoding = %q: unexpected error: %v", enc, err)
		}
	}

	// Invalid encodings.
	for _, enc := range []string{
		"INVALID",
		"UTF-16",
		"ASCII",
		"",
		"latin42",
	} {
		if err := sess.Set("client_encoding", enc, false); err == nil {
			t.Errorf("SET client_encoding = %q: expected error, got nil", enc)
		}
	}
}
