package executor

import (
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFillfactorSurfacesInPgClassReloptions verifies that a `WITH
// (fillfactor=N)` storage parameter declared on CREATE TABLE is persisted on
// the catalog table and surfaced through the pg_class virtual view's reloptions
// cell as the text[] literal `{fillfactor=N}`. pg_dump renders that array back
// as `WITH (fillfactor='N')`, so this is the engine-side half of the round-trip
// the pg_dump TAP port (TestPort_PgDumpConnectionSetup, slice 54) asserts
// end-to-end. DU-002 slice 54.
func TestFillfactorSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE opt (id integer PRIMARY KEY) WITH (fillfactor=70)`); err != nil {
		t.Fatalf("CREATE TABLE opt: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE plain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE plain: %v", err)
	}

	optTbl, ok := cat.LookupTable(parser.ObjectName{Name: "opt"})
	if !ok {
		t.Fatal("opt table not found")
	}
	if optTbl.Fillfactor != 70 {
		t.Errorf("opt.Fillfactor = %d, want 70", optTbl.Fillfactor)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "plain"})
	if !ok {
		t.Fatal("plain table not found")
	}
	if plainTbl.Fillfactor != 0 {
		t.Errorf("plain.Fillfactor = %d, want 0 (unset)", plainTbl.Fillfactor)
	}

	// pg_class.reloptions (column index 32) must read `{fillfactor=70}` for the
	// option-bearing table and "" (→ SQL NULL) for the plain one.
	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "opt" || r[1] == "plain") {
			got[r[1]] = r[32]
		}
	}
	if got["opt"] != "{fillfactor=70}" {
		t.Errorf("pg_class.reloptions for opt = %q, want %q", got["opt"], "{fillfactor=70}")
	}
	if got["plain"] != "" {
		t.Errorf("pg_class.reloptions for plain = %q, want \"\" (NULL)", got["plain"])
	}
}

// TestParallelWorkersSurfacesInPgClassReloptions verifies that a `WITH
// (parallel_workers=N)` storage parameter declared on CREATE TABLE is persisted
// on the catalog table and surfaced through the pg_class virtual view's
// reloptions cell. Three cases pin the behavior: a value alongside fillfactor
// (combined `{fillfactor=70,parallel_workers=4}`), the edge value 0 (a VALID
// explicit setting that must still emit `parallel_workers=0`, distinct from
// unset), and a plain table (no reloptions). pg_dump renders the array back as
// `WITH (parallel_workers='N')`. goopg has no parallel query, so the value is
// catalog/dump-only. DU-002 slice 195.
func TestParallelWorkersSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pw (id integer PRIMARY KEY) WITH (fillfactor=70, parallel_workers=4)`); err != nil {
		t.Fatalf("CREATE TABLE pw: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE pwzero (id integer PRIMARY KEY) WITH (parallel_workers=0)`); err != nil {
		t.Fatalf("CREATE TABLE pwzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE pwplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE pwplain: %v", err)
	}

	pwTbl, ok := cat.LookupTable(parser.ObjectName{Name: "pw"})
	if !ok {
		t.Fatal("pw table not found")
	}
	if !pwTbl.ParallelWorkersSet || pwTbl.ParallelWorkers != 4 {
		t.Errorf("pw.ParallelWorkers = %d (set=%v), want 4 (set=true)", pwTbl.ParallelWorkers, pwTbl.ParallelWorkersSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "pwzero"})
	if !ok {
		t.Fatal("pwzero table not found")
	}
	if !zeroTbl.ParallelWorkersSet || zeroTbl.ParallelWorkers != 0 {
		t.Errorf("pwzero.ParallelWorkers = %d (set=%v), want 0 (set=true)", zeroTbl.ParallelWorkers, zeroTbl.ParallelWorkersSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "pwplain"})
	if !ok {
		t.Fatal("pwplain table not found")
	}
	if plainTbl.ParallelWorkersSet {
		t.Errorf("pwplain.ParallelWorkersSet = true, want false (unset)")
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "pw" || r[1] == "pwzero" || r[1] == "pwplain") {
			got[r[1]] = r[32]
		}
	}
	if got["pw"] != "{fillfactor=70,parallel_workers=4}" {
		t.Errorf("pg_class.reloptions for pw = %q, want %q", got["pw"], "{fillfactor=70,parallel_workers=4}")
	}
	if got["pwzero"] != "{parallel_workers=0}" {
		t.Errorf("pg_class.reloptions for pwzero = %q, want %q", got["pwzero"], "{parallel_workers=0}")
	}
	if got["pwplain"] != "" {
		t.Errorf("pg_class.reloptions for pwplain = %q, want \"\" (NULL)", got["pwplain"])
	}
}

// TestParallelWorkersOutOfBoundsRejected verifies CREATE TABLE rejects a
// parallel_workers value outside the valid 0–1024 range (or a non-integer) with
// PG's 22023 error. DU-002 slice 195.
func TestParallelWorkersOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, pw := range []string{"1025", "99999"} {
		err := runDDL(t, ctx, `CREATE TABLE pwbad`+strconv.Itoa(i)+` (id integer) WITH (parallel_workers=`+pw+`)`)
		if err == nil {
			t.Errorf("parallel_workers=%s: expected an out-of-bounds error, got nil", pw)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("parallel_workers=%s: error type = %T, want *ExecError", pw, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("parallel_workers=%s: error code = %q, want 22023", pw, ee.Code)
		}
	}
}

// TestAutovacuumEnabledSurfacesInPgClassReloptions verifies that a `WITH
// (autovacuum_enabled=BOOL)` storage parameter declared on CREATE TABLE is
// persisted on the catalog table and surfaced through the pg_class virtual
// view's reloptions cell. Cases pin: a false value alongside fillfactor
// (combined `{fillfactor=70,autovacuum_enabled=false}`), a true value spelled
// with a non-canonical accepted token ("on" → `autovacuum_enabled=true`), and a
// plain table (no reloptions). pg_dump renders the array back as `WITH
// (autovacuum_enabled='false')`. goopg has no autovacuum, so the value is
// catalog/dump-only. DU-002 slice 196.
func TestAutovacuumEnabledSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_enabled=false)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avon (id integer PRIMARY KEY) WITH (autovacuum_enabled=on)`); err != nil {
		t.Fatalf("CREATE TABLE avon: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if !avTbl.AutovacuumEnabledSet || avTbl.AutovacuumEnabled {
		t.Errorf("av.AutovacuumEnabled = %v (set=%v), want false (set=true)", avTbl.AutovacuumEnabled, avTbl.AutovacuumEnabledSet)
	}
	onTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avon"})
	if !ok {
		t.Fatal("avon table not found")
	}
	if !onTbl.AutovacuumEnabledSet || !onTbl.AutovacuumEnabled {
		t.Errorf("avon.AutovacuumEnabled = %v (set=%v), want true (set=true)", onTbl.AutovacuumEnabled, onTbl.AutovacuumEnabledSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumEnabledSet {
		t.Errorf("avplain.AutovacuumEnabledSet = true, want false (unset)")
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avon" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_enabled=false}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_enabled=false}")
	}
	if got["avon"] != "{autovacuum_enabled=true}" {
		t.Errorf("pg_class.reloptions for avon = %q, want %q", got["avon"], "{autovacuum_enabled=true}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumEnabledInvalidValueRejected verifies CREATE TABLE rejects a
// non-boolean autovacuum_enabled value with PG's 22023 error. DU-002 slice 196.
func TestAutovacuumEnabledInvalidValueRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"maybe", "2", "tru3"} {
		err := runDDL(t, ctx, `CREATE TABLE avbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_enabled=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_enabled=%s: expected an invalid-value error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_enabled=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_enabled=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestToastTupleTargetSurfacesInPgClassReloptions verifies that a `WITH
// (toast_tuple_target=N)` storage parameter declared on CREATE TABLE is
// persisted on the catalog table and surfaced through the pg_class virtual
// view's reloptions cell. Cases pin: a value alongside fillfactor (combined
// `{fillfactor=70,toast_tuple_target=256}`), a boundary value at the minimum
// (128), and a plain table (no reloptions). pg_dump renders the array back as
// `WITH (toast_tuple_target='256')`. goopg's TOAST thresholds are fixed, so the
// value is catalog/dump-only. DU-002 slice 197.
func TestToastTupleTargetSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE tt (id integer PRIMARY KEY) WITH (fillfactor=70, toast_tuple_target=256)`); err != nil {
		t.Fatalf("CREATE TABLE tt: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE ttmin (id integer PRIMARY KEY) WITH (toast_tuple_target=128)`); err != nil {
		t.Fatalf("CREATE TABLE ttmin: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE ttplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE ttplain: %v", err)
	}

	ttTbl, ok := cat.LookupTable(parser.ObjectName{Name: "tt"})
	if !ok {
		t.Fatal("tt table not found")
	}
	if ttTbl.ToastTupleTarget != 256 {
		t.Errorf("tt.ToastTupleTarget = %d, want 256", ttTbl.ToastTupleTarget)
	}
	minTbl, ok := cat.LookupTable(parser.ObjectName{Name: "ttmin"})
	if !ok {
		t.Fatal("ttmin table not found")
	}
	if minTbl.ToastTupleTarget != 128 {
		t.Errorf("ttmin.ToastTupleTarget = %d, want 128", minTbl.ToastTupleTarget)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "ttplain"})
	if !ok {
		t.Fatal("ttplain table not found")
	}
	if plainTbl.ToastTupleTarget != 0 {
		t.Errorf("ttplain.ToastTupleTarget = %d, want 0 (unset)", plainTbl.ToastTupleTarget)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "tt" || r[1] == "ttmin" || r[1] == "ttplain") {
			got[r[1]] = r[32]
		}
	}
	if got["tt"] != "{fillfactor=70,toast_tuple_target=256}" {
		t.Errorf("pg_class.reloptions for tt = %q, want %q", got["tt"], "{fillfactor=70,toast_tuple_target=256}")
	}
	if got["ttmin"] != "{toast_tuple_target=128}" {
		t.Errorf("pg_class.reloptions for ttmin = %q, want %q", got["ttmin"], "{toast_tuple_target=128}")
	}
	if got["ttplain"] != "" {
		t.Errorf("pg_class.reloptions for ttplain = %q, want \"\" (NULL)", got["ttplain"])
	}
}

// TestToastTupleTargetOutOfBoundsRejected verifies CREATE TABLE rejects a
// toast_tuple_target value outside the valid 128–8160 range (or a non-integer)
// with PG's 22023 error. DU-002 slice 197.
func TestToastTupleTargetOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, tt := range []string{"127", "8161", "0", "huge"} {
		err := runDDL(t, ctx, `CREATE TABLE ttbad`+strconv.Itoa(i)+` (id integer) WITH (toast_tuple_target=`+tt+`)`)
		if err == nil {
			t.Errorf("toast_tuple_target=%s: expected an out-of-bounds error, got nil", tt)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("toast_tuple_target=%s: error type = %T, want *ExecError", tt, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("toast_tuple_target=%s: error code = %q, want 22023", tt, ee.Code)
		}
	}
}

// TestFillfactorOutOfBoundsRejected verifies CREATE TABLE rejects a fillfactor
// outside the valid 10–100 range with PG's 22023 error, mirroring the existing
// CREATE INDEX bounds check. DU-002 slice 54.
func TestFillfactorOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ff := range []string{"5", "0", "101"} {
		err := runDDL(t, ctx, `CREATE TABLE bad`+ff+` (id integer) WITH (fillfactor=`+ff+`)`)
		if err == nil {
			t.Errorf("fillfactor=%s: expected an out-of-bounds error, got nil", ff)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("fillfactor=%s: error type = %T, want *ExecError", ff, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("fillfactor=%s: error code = %q, want 22023", ff, ee.Code)
		}
	}
}
