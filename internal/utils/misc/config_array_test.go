package misc

import "testing"

// TestConfigArrayItem pins GUCArrayAdd / GUCArrayDelete's front half
// (validate_option_array_item + find_option name normalisation) as PG 18.3
// shows it through ALTER ROLE ... SET / RESET and CREATE FUNCTION ... SET:
// the stored name is the variable's own spelling (map_old_guc_names
// applied), a custom name keeps its spelling, and bad names, contexts and
// values are rejected with PostgreSQL's SQLSTATEs.
func TestConfigArrayItem(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		name  string
		value *string
		want  string
	}{
		{"datestyle", str("iso, mdy"), "DateStyle"},
		{"TIMEZONE", str("UTC"), "TimeZone"},
		{"intervalstyle", str("iso_8601"), "IntervalStyle"},
		{"work_mem", str("1MB"), "work_mem"},
		{"SORT_MEM", str("2000"), "work_mem"},
		{"vacuum_mem", str("1MB"), "maintenance_work_mem"},
		{"log_statement", str("all"), "log_statement"},
		{"my.X", str("y"), "my.X"},
		{"datestyle", nil, "DateStyle"}, // RESET: name only
		// PostgreSQL parameters goopg does not register: accepted by name
		// and context, stored under PG's spelling.
		{"role", str("r1"), "role"},
		{"temp_tablespaces", str("ts1"), "temp_tablespaces"},
		{"Log_Min_Error_Statement", str("error"), "log_min_error_statement"},
	} {
		got, err := ConfigArrayItem(c.name, c.value)
		if err != nil || got != c.want {
			t.Errorf("ConfigArrayItem(%q) = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct {
		name, code, msg string
		value           *string
	}{
		{"no_such_guc", "42704", `unrecognized configuration parameter "no_such_guc"`, str("1")},
		{"no_such_guc", "42704", `unrecognized configuration parameter "no_such_guc"`, nil},
		{"server_version", "55P02", `parameter "server_version" cannot be changed`, str("1")},
		{"shared_buffers", "55P02", `parameter "shared_buffers" cannot be changed without restarting the server`, nil},
		{"checkpoint_timeout", "55P02", `parameter "checkpoint_timeout" cannot be changed now`, str("1min")},
		// PG contexts for parameters goopg lacks or registers differently.
		{"log_connections", "55P02", `parameter "log_connections" cannot be set after connection start`, str("receipt")},
		{"wal_writer_delay", "55P02", `parameter "wal_writer_delay" cannot be changed now`, str("300ms")},
		{"archive_mode", "55P02", `parameter "archive_mode" cannot be changed without restarting the server`, str("on")},
		{"work_mem", "22023", "", str("bogus")},
	} {
		_, err := ConfigArrayItem(c.name, c.value)
		aerr, ok := err.(*AlterSystemError)
		if !ok || aerr.Code != c.code || (c.msg != "" && aerr.Msg != c.msg) {
			t.Errorf("ConfigArrayItem(%q) error = %#v; want %s %q", c.name, err, c.code, c.msg)
		}
	}
}

// TestPGGUCTable pins pg_gucs_gen.go to the oracle build's parameter set:
// pg_settings' 401 rows plus the six GUC_NO_SHOW_ALL parameters, including
// the DEBUG_NODE_TESTS_ENABLED ones the assert-enabled oracle compiles in and
// excluding parameters that exist only in other debug builds. Entries whose
// context sits on the line after the name were once missed by the
// extraction (io_workers, max_worker_processes).
func TestPGGUCTable(t *testing.T) {
	if len(pgGUCs) != 407 {
		t.Errorf("pgGUCs has %d entries, want 407", len(pgGUCs))
	}
	for _, n := range []string{"io_workers", "max_worker_processes", "role", "seed",
		"session_authorization", "debug_copy_parse_plan_trees", "datestyle"} {
		if _, ok := pgGUCs[n]; !ok {
			t.Errorf("pgGUCs lacks %q", n)
		}
	}
	for _, n := range []string{"trace_locks", "wal_debug", "debug_deadlocks", "max_predicate_locks_per_page"} {
		if _, ok := pgGUCs[n]; ok {
			t.Errorf("pgGUCs has %q, which the oracle build does not know", n)
		}
	}
	if g := pgGUCs["io_workers"]; g.Context != ContextSigHup {
		t.Errorf("io_workers context = %v, want sighup", g.Context)
	}
}
