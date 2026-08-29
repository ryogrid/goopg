package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0160 guard suite for the reloption name/namespace registry.
//
// The bug this pins: goopg validated storage parameters only by *recognising*
// them, so a name no `if v, ok := s.With["…"]` block looked for was silently
// accepted and dropped. `CREATE TABLE t(i int) WITH (not_existing_option=2)`
// SUCCEEDED where PG raises `unrecognized parameter "not_existing_option"`
// (22023, postgres/src/backend/access/common/reloptions.c:1488), and
// `WITH (bad_ns.fillfactor=2)` succeeded where PG raises `unrecognized
// parameter namespace "bad_ns"` (:1275).
//
// The registry is asserted directly rather than only end-to-end because it is
// shared by five call sites with different admissible sets (heap CREATE TABLE,
// CTAS, partition child, CREATE INDEX per access method, ALTER … SET), and a
// table-driven end-to-end suite would not cover the per-kind asymmetries at all.

// TestRelOptionRegistryKindMembership pins the per-kind admissible sets against
// PG 18.3's five reloption tables — in particular the asymmetries that are easy
// to get wrong because they look like omissions: parallel_workers /
// toast_tuple_target / user_catalog_table / autovacuum_analyze_* are HEAP-only,
// so their `toast.`-qualified spellings are errors upstream.
func TestRelOptionRegistryKindMembership(t *testing.T) {
	cases := []struct {
		name string
		kind relOptKind
		ok   bool
	}{
		// Heap.
		{"fillfactor", relOptHeap, true},
		{"parallel_workers", relOptHeap, true},
		{"autovacuum_enabled", relOptHeap, true},
		{"vacuum_max_eager_freeze_failure_rate", relOptHeap, true},
		{"security_barrier", relOptHeap, false},
		{"deduplicate_items", relOptHeap, false},
		{"not_existing_option", relOptHeap, false},
		// TOAST — the HEAP-only asymmetries.
		{"autovacuum_vacuum_threshold", relOptToast, true},
		{"vacuum_truncate", relOptToast, true},
		{"parallel_workers", relOptToast, false},
		{"toast_tuple_target", relOptToast, false},
		{"user_catalog_table", relOptToast, false},
		{"autovacuum_analyze_threshold", relOptToast, false},
		{"autovacuum_analyze_scale_factor", relOptToast, false},
		{"fillfactor", relOptToast, false},
		// Index access methods.
		{"fillfactor", relOptBTree, true},
		{"deduplicate_items", relOptBTree, true},
		{"vacuum_cleanup_index_scale_factor", relOptBTree, true},
		{"buffering", relOptBTree, false},
		{"fastupdate", relOptBTree, false},
		{"buffering", relOptGiST, true},
		{"fillfactor", relOptGiST, true},
		{"fastupdate", relOptGIN, true},
		{"gin_pending_list_limit", relOptGIN, true},
		{"fillfactor", relOptGIN, false}, // GIN has no fillfactor upstream
		{"pages_per_range", relOptBRIN, true},
		{"autosummarize", relOptBRIN, true},
		{"fillfactor", relOptBRIN, false}, // nor does BRIN
		{"fillfactor", relOptHash, true},
		{"fillfactor", relOptSPGiST, true},
		// View.
		{"security_barrier", relOptView, true},
		{"security_invoker", relOptView, true},
		{"check_option", relOptView, true},
		{"fillfactor", relOptView, false},
	}
	for _, c := range cases {
		got := relOptionKinds[c.name]&c.kind != 0
		if got != c.ok {
			t.Errorf("relOptionKinds[%q] admits kind %d = %v, want %v", c.name, c.kind, got, c.ok)
		}
	}
}

// TestValidateRelOptionNamesRejectsUnknown covers the two error shapes and the
// pass-ordering rule: PG runs transformRelOptions (namespaces, whole list) to
// completion before parseRelOptions (names), so a clause carrying both faults
// reports the NAMESPACE one.
func TestValidateRelOptionNamesRejectsUnknown(t *testing.T) {
	cases := []struct {
		desc          string
		names         []string
		kind          relOptKind
		allowNsps     bool
		acceptOidsOff bool
		wantErrMsg    string // "" = must be accepted
	}{
		{"recognized heap option", []string{"fillfactor"}, relOptHeap, true, false, ""},
		{"recognized toast-namespaced option", []string{"toast.autovacuum_enabled"}, relOptHeap, true, false, ""},
		{"unknown name", []string{"not_existing_option"}, relOptHeap, true, false,
			`unrecognized parameter "not_existing_option"`},
		{"unknown namespace", []string{"not_existing_namespace.fillfactor"}, relOptHeap, true, false,
			`unrecognized parameter namespace "not_existing_namespace"`},
		{"heap-only option in toast namespace", []string{"toast.parallel_workers"}, relOptHeap, true, false,
			`unrecognized parameter "parallel_workers"`},
		{"double-quoted mixed case is not a recognized name", []string{"Fillfactor"}, relOptHeap, true, false,
			`unrecognized parameter "Fillfactor"`},
		// DefineIndex passes validnsps = NULL, so `toast` is unrecognized there
		// even though it is the one namespace heap relations declare.
		{"index rejects every namespace", []string{"toast.fillfactor"}, relOptBTree, false, false,
			`unrecognized parameter namespace "toast"`},
		{"index recognizes its AM's option", []string{"fillfactor"}, relOptBTree, false, false, ""},
		{"index rejects another AM's option", []string{"fastupdate"}, relOptBTree, false, false,
			`unrecognized parameter "fastupdate"`},
		// Ordering: namespace pass runs first over the whole list.
		{"namespace fault beats name fault", []string{"aaa_bad_name", "zz_bad_ns.fillfactor"}, relOptHeap, true, false,
			`unrecognized parameter namespace "zz_bad_ns"`},
		{"empty clause", nil, relOptHeap, true, false, ""},
		// acceptOidsOff: DefineRelation filters the legacy `WITH (oids = false)`
		// no-op out before validation (reloptions.c:1307-1322); ALTER/index paths
		// pass false and reject it like any other unknown name.
		{"oids skipped when acceptOidsOff", []string{"oids"}, relOptHeap, true, true, ""},
		{"oids rejected otherwise", []string{"oids"}, relOptHeap, true, false,
			`unrecognized parameter "oids"`},
		{"qualified oids is never skipped", []string{"toast.oids"}, relOptHeap, true, true,
			`unrecognized parameter "oids"`},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			err := validateRelOptionNames(c.names, c.kind, c.allowNsps, c.acceptOidsOff, 7)
			if c.wantErrMsg == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %q, got nil", c.wantErrMsg)
			}
			if err.Code != "22023" {
				t.Errorf("code = %q, want 22023", err.Code)
			}
			if err.Message != c.wantErrMsg {
				t.Errorf("message = %q, want %q", err.Message, c.wantErrMsg)
			}
			if err.Pos != 7 {
				t.Errorf("Pos = %d, want the caller's 7", err.Pos)
			}
		})
	}
}

// TestIndexRelOptKindPerAccessMethod pins the AM -> kind mapping, including the
// two edges that matter: an omitted USING clause is btree (gram.y defaults
// access_method_clause to DEFAULT_INDEX_TYPE), and an unknown AM admits nothing
// rather than defaulting to a permissive set.
func TestIndexRelOptKindPerAccessMethod(t *testing.T) {
	cases := map[string]relOptKind{
		"":           relOptBTree,
		"btree":      relOptBTree,
		"BTREE":      relOptBTree,
		"hash":       relOptHash,
		"gist":       relOptGiST,
		"spgist":     relOptSPGiST,
		"gin":        relOptGIN,
		"brin":       relOptBRIN,
		"no_such_am": 0,
		"  btree   ": relOptBTree,
	}
	for method, want := range cases {
		if got := indexRelOptKind(method); got != want {
			t.Errorf("indexRelOptKind(%q) = %d, want %d", method, got, want)
		}
	}
}

// TestCreateRelOptionsRejectedEndToEnd is the executor-level half: the registry
// must actually be reached from CREATE TABLE and CREATE INDEX, and — the point
// of the fix — the rejected statement must NOT have created the relation. The
// cascade is what made reloptions.sql unreadable: a silently-accepted option
// created the table, so every later negative case reported "relation already
// exists" instead of its own error.
func TestCreateRelOptionsRejectedEndToEnd(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, c := range []struct {
		stmt    string
		wantMsg string
	}{
		{`CREATE TABLE rej(i int) WITH (not_existing_option=2)`,
			`unrecognized parameter "not_existing_option"`},
		{`CREATE TABLE rej(i int) WITH (not_existing_namespace.fillfactor=2)`,
			`unrecognized parameter namespace "not_existing_namespace"`},
		{`CREATE TABLE rej(i int) WITH (toast.not_existing_option=42)`,
			`unrecognized parameter "not_existing_option"`},
	} {
		err := runDDL(t, ctx, c.stmt)
		if err == nil {
			t.Fatalf("%s: expected rejection, got nil", c.stmt)
		}
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "22023" || ee.Message != c.wantMsg {
			t.Errorf("%s: error = %v, want 22023 %q", c.stmt, err, c.wantMsg)
		}
		// The cascade guard: nothing may have been created.
		if _, exists := cat.LookupTable(parser.ObjectName{Name: "rej"}); exists {
			t.Fatalf("%s: relation was created despite the error", c.stmt)
		}
	}

	// A recognized clause still works, and CREATE INDEX validates against its
	// access method's set BEFORE the duplicate-name check (DefineIndex order).
	if err := runDDL(t, ctx, `CREATE TABLE ok_t(s varchar) WITH (fillfactor=40)`); err != nil {
		t.Fatalf("CREATE TABLE ok_t: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE INDEX ok_idx ON ok_t (s) WITH (fillfactor=30)`); err != nil {
		t.Fatalf("CREATE INDEX ok_idx: %v", err)
	}
	for _, c := range []struct {
		stmt    string
		wantMsg string
	}{
		{`CREATE INDEX ok_idx ON ok_t (s) WITH (not_existing_option=2)`,
			`unrecognized parameter "not_existing_option"`},
		{`CREATE INDEX ok_idx ON ok_t (s) WITH (not_existing_ns.fillfactor=2)`,
			`unrecognized parameter namespace "not_existing_ns"`},
	} {
		err := runDDL(t, ctx, c.stmt)
		if err == nil {
			t.Fatalf("%s: expected rejection, got nil", c.stmt)
		}
		ee, ok := err.(*ExecError)
		if !ok || ee.Message != c.wantMsg {
			// "relation already exists" here means the reloption check ran too
			// late — the exact ordering bug reloptions.sql catches.
			t.Errorf("%s: error = %v, want %q", c.stmt, err, c.wantMsg)
		}
	}
}
