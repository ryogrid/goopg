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

// TestAutovacuumVacuumThresholdSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_vacuum_threshold=N)` storage parameter declared on CREATE
// TABLE is persisted on the catalog table and surfaced through the pg_class
// virtual view's reloptions cell. Like parallel_workers (slice 195), 0 is a
// valid explicit value (PG's reloption default is -1 = unset), so the Set flag
// — not a zero check — guards presence. Cases pin: a value alongside fillfactor
// (combined `{fillfactor=70,autovacuum_vacuum_threshold=100}`), the boundary
// value 0 (explicitly set), and a plain table (no reloptions). pg_dump renders
// the array back as `WITH (autovacuum_vacuum_threshold='100')`. goopg has no
// autovacuum, so the value is catalog/dump-only. DU-002 slice 198.
func TestAutovacuumVacuumThresholdSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_threshold=100)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_threshold=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumThreshold != 100 || !avTbl.AutovacuumVacuumThresholdSet {
		t.Errorf("av threshold = %d set=%v, want 100 set=true", avTbl.AutovacuumVacuumThreshold, avTbl.AutovacuumVacuumThresholdSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumThreshold != 0 || !zeroTbl.AutovacuumVacuumThresholdSet {
		t.Errorf("avzero threshold = %d set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumThreshold, zeroTbl.AutovacuumVacuumThresholdSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumThresholdSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumThresholdSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_threshold=100}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_threshold=100}")
	}
	if got["avzero"] != "{autovacuum_vacuum_threshold=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_threshold=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumThresholdOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-INT_MAX or non-integer autovacuum_vacuum_threshold with PG's
// 22023 error. The valid range is 0–INT_MAX (negatives are rejected earlier by
// the parser as a syntax error, so the reachable invalid cases are overflow and
// non-integer). DU-002 slice 198.
func TestAutovacuumVacuumThresholdOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"9999999999", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE avbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_threshold=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_threshold=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_threshold=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_threshold=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestAutovacuumVacuumScaleFactorSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_vacuum_scale_factor=F)` storage parameter — the first
// REAL-typed reloption goopg round-trips — declared on CREATE TABLE is persisted
// on the catalog table and surfaced through the pg_class virtual view's
// reloptions cell. Like parallel_workers (slice 195), 0.0 is a valid explicit
// value (PG's reloption default is -1 = unset), so the Set flag — not a zero
// check — guards presence. Cases pin: a fractional value alongside fillfactor
// (combined `{fillfactor=70,autovacuum_vacuum_scale_factor=0.2}`), the boundary
// value 0 (explicitly set, rendered `=0`), and a plain table (no reloptions).
// pg_dump renders the array back as `WITH
// (autovacuum_vacuum_scale_factor='0.2')`. goopg has no autovacuum, so the value
// is catalog/dump-only. DU-002 slice 199.
func TestAutovacuumVacuumScaleFactorSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_scale_factor=0.2)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_scale_factor=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumScaleFactor != 0.2 || !avTbl.AutovacuumVacuumScaleFactorSet {
		t.Errorf("av scale_factor = %v set=%v, want 0.2 set=true", avTbl.AutovacuumVacuumScaleFactor, avTbl.AutovacuumVacuumScaleFactorSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumScaleFactor != 0 || !zeroTbl.AutovacuumVacuumScaleFactorSet {
		t.Errorf("avzero scale_factor = %v set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumScaleFactor, zeroTbl.AutovacuumVacuumScaleFactorSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumScaleFactorSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumScaleFactorSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_scale_factor=0.2}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_scale_factor=0.2}")
	}
	if got["avzero"] != "{autovacuum_vacuum_scale_factor=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_scale_factor=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumScaleFactorOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-100.0 or non-numeric autovacuum_vacuum_scale_factor with PG's
// 22023 error. The valid range is 0.0–100.0 (negatives are rejected earlier by
// the parser as a syntax error, so the reachable invalid cases are above-range
// and non-numeric). DU-002 slice 199.
func TestAutovacuumVacuumScaleFactorOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"100.5", "1000", "notafloat"} {
		err := runDDL(t, ctx, `CREATE TABLE avsfbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_scale_factor=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_scale_factor=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_scale_factor=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_scale_factor=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestAutovacuumAnalyzeScaleFactorSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_analyze_scale_factor=F)` storage parameter — the second
// REAL-typed reloption goopg round-trips, reusing the slice-199 float path —
// declared on CREATE TABLE is persisted on the catalog table and surfaced
// through the pg_class virtual view's reloptions cell. Like
// autovacuum_vacuum_scale_factor (slice 199), 0.0 is a valid explicit value
// (PG's reloption default is -1 = unset), so the Set flag — not a zero check —
// guards presence. Cases pin: a fractional value alongside fillfactor
// (combined `{fillfactor=70,autovacuum_analyze_scale_factor=0.05}`), the
// boundary value 0 (explicitly set, rendered `=0`), and a plain table (no
// reloptions). pg_dump renders the array back as
// `WITH (autovacuum_analyze_scale_factor='0.05')`. goopg has no autovacuum, so
// the value is catalog/dump-only. DU-002 slice 200.
func TestAutovacuumAnalyzeScaleFactorSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE aa (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_analyze_scale_factor=0.05)`); err != nil {
		t.Fatalf("CREATE TABLE aa: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE aazero (id integer PRIMARY KEY) WITH (autovacuum_analyze_scale_factor=0)`); err != nil {
		t.Fatalf("CREATE TABLE aazero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE aaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE aaplain: %v", err)
	}

	aaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "aa"})
	if !ok {
		t.Fatal("aa table not found")
	}
	if aaTbl.AutovacuumAnalyzeScaleFactor != 0.05 || !aaTbl.AutovacuumAnalyzeScaleFactorSet {
		t.Errorf("aa analyze_scale_factor = %v set=%v, want 0.05 set=true", aaTbl.AutovacuumAnalyzeScaleFactor, aaTbl.AutovacuumAnalyzeScaleFactorSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "aazero"})
	if !ok {
		t.Fatal("aazero table not found")
	}
	if zeroTbl.AutovacuumAnalyzeScaleFactor != 0 || !zeroTbl.AutovacuumAnalyzeScaleFactorSet {
		t.Errorf("aazero analyze_scale_factor = %v set=%v, want 0 set=true", zeroTbl.AutovacuumAnalyzeScaleFactor, zeroTbl.AutovacuumAnalyzeScaleFactorSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "aaplain"})
	if !ok {
		t.Fatal("aaplain table not found")
	}
	if plainTbl.AutovacuumAnalyzeScaleFactorSet {
		t.Errorf("aaplain set=%v, want false (unset)", plainTbl.AutovacuumAnalyzeScaleFactorSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "aa" || r[1] == "aazero" || r[1] == "aaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["aa"] != "{fillfactor=70,autovacuum_analyze_scale_factor=0.05}" {
		t.Errorf("pg_class.reloptions for aa = %q, want %q", got["aa"], "{fillfactor=70,autovacuum_analyze_scale_factor=0.05}")
	}
	if got["aazero"] != "{autovacuum_analyze_scale_factor=0}" {
		t.Errorf("pg_class.reloptions for aazero = %q, want %q", got["aazero"], "{autovacuum_analyze_scale_factor=0}")
	}
	if got["aaplain"] != "" {
		t.Errorf("pg_class.reloptions for aaplain = %q, want \"\" (NULL)", got["aaplain"])
	}
}

// TestAutovacuumAnalyzeScaleFactorOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-100.0 or non-numeric autovacuum_analyze_scale_factor with
// PG's 22023 error. The valid range is 0.0–100.0 (negatives are rejected earlier
// by the parser as a syntax error, so the reachable invalid cases are above-range
// and non-numeric). DU-002 slice 200.
func TestAutovacuumAnalyzeScaleFactorOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, aa := range []string{"100.5", "1000", "notafloat"} {
		err := runDDL(t, ctx, `CREATE TABLE aasfbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_analyze_scale_factor=`+aa+`)`)
		if err == nil {
			t.Errorf("autovacuum_analyze_scale_factor=%s: expected an error, got nil", aa)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_analyze_scale_factor=%s: error type = %T, want *ExecError", aa, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_analyze_scale_factor=%s: error code = %q, want 22023", aa, ee.Code)
		}
	}
}

// TestAutovacuumVacuumInsertScaleFactorSurfacesInPgClassReloptions verifies that
// a `WITH (autovacuum_vacuum_insert_scale_factor=F)` storage parameter — the
// third REAL-typed reloption goopg round-trips, reusing the slice-199 float path —
// declared on CREATE TABLE is persisted on the catalog table and surfaced through
// the pg_class virtual view's reloptions cell. Like autovacuum_vacuum_scale_factor
// (slice 199), 0.0 is a valid explicit value (PG's reloption default is -1 =
// unset), so the Set flag — not a zero check — guards presence. Cases pin: a
// fractional value alongside fillfactor (combined
// `{fillfactor=70,autovacuum_vacuum_insert_scale_factor=0.2}`), the boundary value
// 0 (explicitly set, rendered `=0`), and a plain table (no reloptions). pg_dump
// renders the array back as `WITH (autovacuum_vacuum_insert_scale_factor='0.2')`.
// goopg has no autovacuum, so the value is catalog/dump-only. DU-002 slice 201.
func TestAutovacuumVacuumInsertScaleFactorSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_insert_scale_factor=0.2)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_insert_scale_factor=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumInsertScaleFactor != 0.2 || !avTbl.AutovacuumVacuumInsertScaleFactorSet {
		t.Errorf("av insert_scale_factor = %v set=%v, want 0.2 set=true", avTbl.AutovacuumVacuumInsertScaleFactor, avTbl.AutovacuumVacuumInsertScaleFactorSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumInsertScaleFactor != 0 || !zeroTbl.AutovacuumVacuumInsertScaleFactorSet {
		t.Errorf("avzero insert_scale_factor = %v set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumInsertScaleFactor, zeroTbl.AutovacuumVacuumInsertScaleFactorSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumInsertScaleFactorSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumInsertScaleFactorSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_insert_scale_factor=0.2}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_insert_scale_factor=0.2}")
	}
	if got["avzero"] != "{autovacuum_vacuum_insert_scale_factor=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_insert_scale_factor=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumInsertScaleFactorOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-100.0 or non-numeric autovacuum_vacuum_insert_scale_factor with
// PG's 22023 error. The valid range is 0.0–100.0 (negatives are rejected earlier
// by the parser as a syntax error, so the reachable invalid cases are above-range
// and non-numeric). DU-002 slice 201.
func TestAutovacuumVacuumInsertScaleFactorOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"100.5", "1000", "notafloat"} {
		err := runDDL(t, ctx, `CREATE TABLE avisfbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_insert_scale_factor=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_insert_scale_factor=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_insert_scale_factor=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_insert_scale_factor=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestAutovacuumVacuumCostDelaySurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_vacuum_cost_delay=F)` storage parameter — the fourth (and
// final) REAL-typed reloption goopg round-trips, reusing the slice-199 float path —
// declared on CREATE TABLE is persisted on the catalog table and surfaced through
// the pg_class virtual view's reloptions cell. Like autovacuum_vacuum_scale_factor
// (slice 199), 0.0 is a valid explicit value (PG's reloption default is -1 =
// unset), so the Set flag — not a zero check — guards presence. Cases pin: a
// fractional value alongside fillfactor (combined
// `{fillfactor=70,autovacuum_vacuum_cost_delay=2.5}`), the boundary value 0
// (explicitly set, rendered `=0`), and a plain table (no reloptions). pg_dump
// renders the array back as `WITH (autovacuum_vacuum_cost_delay='2.5')`. goopg has
// no autovacuum, so the value is catalog/dump-only. DU-002 slice 202.
func TestAutovacuumVacuumCostDelaySurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_cost_delay=2.5)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_cost_delay=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumCostDelay != 2.5 || !avTbl.AutovacuumVacuumCostDelaySet {
		t.Errorf("av cost_delay = %v set=%v, want 2.5 set=true", avTbl.AutovacuumVacuumCostDelay, avTbl.AutovacuumVacuumCostDelaySet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumCostDelay != 0 || !zeroTbl.AutovacuumVacuumCostDelaySet {
		t.Errorf("avzero cost_delay = %v set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumCostDelay, zeroTbl.AutovacuumVacuumCostDelaySet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumCostDelaySet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumCostDelaySet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_cost_delay=2.5}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_cost_delay=2.5}")
	}
	if got["avzero"] != "{autovacuum_vacuum_cost_delay=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_cost_delay=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumCostDelayOutOfBoundsRejected verifies CREATE TABLE rejects
// an above-100.0 or non-numeric autovacuum_vacuum_cost_delay with PG's 22023 error.
// The valid range is 0.0–100.0 (negatives are rejected earlier by the parser as a
// syntax error, so the reachable invalid cases are above-range and non-numeric).
// DU-002 slice 202.
func TestAutovacuumVacuumCostDelayOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"100.5", "1000", "notafloat"} {
		err := runDDL(t, ctx, `CREATE TABLE avcdbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_cost_delay=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_cost_delay=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_cost_delay=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_cost_delay=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestAutovacuumAnalyzeThresholdSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_analyze_threshold=N)` storage parameter declared on CREATE
// TABLE is persisted on the catalog table and surfaced through the pg_class
// virtual view's reloptions cell. Like autovacuum_vacuum_threshold (slice 198),
// 0 is a valid explicit value (PG's reloption default is -1 = unset), so the Set
// flag — not a zero check — guards presence. Cases pin: a value alongside
// fillfactor (combined `{fillfactor=70,autovacuum_analyze_threshold=50}`), the
// boundary value 0 (explicitly set), and a plain table (no reloptions). pg_dump
// renders the array back as `WITH (autovacuum_analyze_threshold='50')`. goopg
// has no autovacuum, so the value is catalog/dump-only. DU-002 slice 203.
func TestAutovacuumAnalyzeThresholdSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_analyze_threshold=50)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_analyze_threshold=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumAnalyzeThreshold != 50 || !avTbl.AutovacuumAnalyzeThresholdSet {
		t.Errorf("av threshold = %d set=%v, want 50 set=true", avTbl.AutovacuumAnalyzeThreshold, avTbl.AutovacuumAnalyzeThresholdSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumAnalyzeThreshold != 0 || !zeroTbl.AutovacuumAnalyzeThresholdSet {
		t.Errorf("avzero threshold = %d set=%v, want 0 set=true", zeroTbl.AutovacuumAnalyzeThreshold, zeroTbl.AutovacuumAnalyzeThresholdSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumAnalyzeThresholdSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumAnalyzeThresholdSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_analyze_threshold=50}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_analyze_threshold=50}")
	}
	if got["avzero"] != "{autovacuum_analyze_threshold=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_analyze_threshold=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumAnalyzeThresholdOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-INT_MAX or non-integer autovacuum_analyze_threshold with PG's
// 22023 error. The valid range is 0–INT_MAX (negatives are rejected earlier by
// the parser as a syntax error, so the reachable invalid cases are overflow and
// non-integer). DU-002 slice 203.
func TestAutovacuumAnalyzeThresholdOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"9999999999", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE avatbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_analyze_threshold=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_analyze_threshold=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_analyze_threshold=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_analyze_threshold=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestAutovacuumVacuumInsertThresholdSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_vacuum_insert_threshold=N)` storage parameter declared on
// CREATE TABLE is persisted on the catalog table and surfaced through the
// pg_class virtual view's reloptions cell. Like the other INT autovacuum
// reloptions (slices 198/203), 0 is a valid explicit value (PG's reloption
// default is -2 = unset), so the Set flag — not a zero check — guards presence.
// Cases pin: a value alongside fillfactor (combined
// `{fillfactor=70,autovacuum_vacuum_insert_threshold=1000}`), the boundary value
// 0 (explicitly set), and a plain table (no reloptions). pg_dump renders the
// array back as `WITH (autovacuum_vacuum_insert_threshold='1000')`. goopg has no
// autovacuum, so the value is catalog/dump-only. DU-002 slice 204.
func TestAutovacuumVacuumInsertThresholdSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_insert_threshold=1000)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_insert_threshold=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumInsertThreshold != 1000 || !avTbl.AutovacuumVacuumInsertThresholdSet {
		t.Errorf("av threshold = %d set=%v, want 1000 set=true", avTbl.AutovacuumVacuumInsertThreshold, avTbl.AutovacuumVacuumInsertThresholdSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumInsertThreshold != 0 || !zeroTbl.AutovacuumVacuumInsertThresholdSet {
		t.Errorf("avzero threshold = %d set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumInsertThreshold, zeroTbl.AutovacuumVacuumInsertThresholdSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumInsertThresholdSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumInsertThresholdSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_insert_threshold=1000}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_insert_threshold=1000}")
	}
	if got["avzero"] != "{autovacuum_vacuum_insert_threshold=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_insert_threshold=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumInsertThresholdOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-INT_MAX or non-integer autovacuum_vacuum_insert_threshold with
// PG's 22023 error. The valid range is -1–INT_MAX (a bare negative is rejected
// earlier by the parser as a syntax error, so the reachable invalid cases are
// overflow and non-integer). DU-002 slice 204.
func TestAutovacuumVacuumInsertThresholdOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"9999999999", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE avitbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_insert_threshold=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_insert_threshold=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_insert_threshold=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_insert_threshold=%s: error code = %q, want 22023", av, ee.Code)
		}
	}
}

// TestVacuumTruncateSurfacesInPgClassReloptions verifies that a `WITH
// (vacuum_truncate=BOOL)` storage parameter declared on CREATE TABLE is
// persisted on the catalog table and surfaced through the pg_class virtual
// view's reloptions cell. vacuum_truncate is a boolean reloption
// (RELOPT_TYPE_BOOL); cases pin: false alongside fillfactor (combined
// `{fillfactor=70,vacuum_truncate=false}`), the `on` spelling normalizing to
// `true`, and a plain table (no reloptions). pg_dump renders the array back as
// `WITH (vacuum_truncate='false')`. goopg has no VACUUM truncation, so the
// value is catalog/dump-only. DU-002 slice 205.
func TestVacuumTruncateSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE vt (id integer PRIMARY KEY) WITH (fillfactor=70, vacuum_truncate=false)`); err != nil {
		t.Fatalf("CREATE TABLE vt: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE vton (id integer PRIMARY KEY) WITH (vacuum_truncate=on)`); err != nil {
		t.Fatalf("CREATE TABLE vton: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE vtplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE vtplain: %v", err)
	}

	vtTbl, ok := cat.LookupTable(parser.ObjectName{Name: "vt"})
	if !ok {
		t.Fatal("vt table not found")
	}
	if !vtTbl.VacuumTruncateSet || vtTbl.VacuumTruncate {
		t.Errorf("vt.VacuumTruncate = %v (set=%v), want false (set=true)", vtTbl.VacuumTruncate, vtTbl.VacuumTruncateSet)
	}
	onTbl, ok := cat.LookupTable(parser.ObjectName{Name: "vton"})
	if !ok {
		t.Fatal("vton table not found")
	}
	if !onTbl.VacuumTruncateSet || !onTbl.VacuumTruncate {
		t.Errorf("vton.VacuumTruncate = %v (set=%v), want true (set=true)", onTbl.VacuumTruncate, onTbl.VacuumTruncateSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "vtplain"})
	if !ok {
		t.Fatal("vtplain table not found")
	}
	if plainTbl.VacuumTruncateSet {
		t.Errorf("vtplain.VacuumTruncateSet = true, want false (unset)")
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "vt" || r[1] == "vton" || r[1] == "vtplain") {
			got[r[1]] = r[32]
		}
	}
	if got["vt"] != "{fillfactor=70,vacuum_truncate=false}" {
		t.Errorf("pg_class.reloptions for vt = %q, want %q", got["vt"], "{fillfactor=70,vacuum_truncate=false}")
	}
	if got["vton"] != "{vacuum_truncate=true}" {
		t.Errorf("pg_class.reloptions for vton = %q, want %q", got["vton"], "{vacuum_truncate=true}")
	}
	if got["vtplain"] != "" {
		t.Errorf("pg_class.reloptions for vtplain = %q, want \"\" (NULL)", got["vtplain"])
	}
}

// TestVacuumTruncateInvalidValueRejected verifies CREATE TABLE rejects a
// non-boolean vacuum_truncate value with PG's 22023 error. DU-002 slice 205.
func TestVacuumTruncateInvalidValueRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"maybe", "2", "tru3"} {
		err := runDDL(t, ctx, `CREATE TABLE vtbad`+strconv.Itoa(i)+` (id integer) WITH (vacuum_truncate=`+v+`)`)
		if err == nil {
			t.Errorf("vacuum_truncate=%s: expected an invalid-value error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("vacuum_truncate=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("vacuum_truncate=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestLogAutovacuumMinDurationSurfacesInPgClassReloptions verifies that a `WITH
// (log_autovacuum_min_duration=N)` storage parameter declared on CREATE TABLE is
// persisted on the catalog table and surfaced through the pg_class virtual
// reloptions text[] column. Covers a value combined with fillfactor (rendered as
// `{fillfactor=70,log_autovacuum_min_duration=250}`), the boundary value 0
// (explicitly set, logs every action), and a plain table (no reloptions).
// pg_dump renders the array back as `WITH (log_autovacuum_min_duration='250')`.
// goopg has no autovacuum, so the value is catalog/dump-only. DU-002 slice 206.
func TestLogAutovacuumMinDurationSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE lamd (id integer PRIMARY KEY) WITH (fillfactor=70, log_autovacuum_min_duration=250)`); err != nil {
		t.Fatalf("CREATE TABLE lamd: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE lamdzero (id integer PRIMARY KEY) WITH (log_autovacuum_min_duration=0)`); err != nil {
		t.Fatalf("CREATE TABLE lamdzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE lamdplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE lamdplain: %v", err)
	}

	lamdTbl, ok := cat.LookupTable(parser.ObjectName{Name: "lamd"})
	if !ok {
		t.Fatal("lamd table not found")
	}
	if lamdTbl.LogAutovacuumMinDuration != 250 || !lamdTbl.LogAutovacuumMinDurationSet {
		t.Errorf("lamd duration = %d set=%v, want 250 set=true", lamdTbl.LogAutovacuumMinDuration, lamdTbl.LogAutovacuumMinDurationSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "lamdzero"})
	if !ok {
		t.Fatal("lamdzero table not found")
	}
	if zeroTbl.LogAutovacuumMinDuration != 0 || !zeroTbl.LogAutovacuumMinDurationSet {
		t.Errorf("lamdzero duration = %d set=%v, want 0 set=true", zeroTbl.LogAutovacuumMinDuration, zeroTbl.LogAutovacuumMinDurationSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "lamdplain"})
	if !ok {
		t.Fatal("lamdplain table not found")
	}
	if plainTbl.LogAutovacuumMinDurationSet {
		t.Errorf("lamdplain set=%v, want false (unset)", plainTbl.LogAutovacuumMinDurationSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "lamd" || r[1] == "lamdzero" || r[1] == "lamdplain") {
			got[r[1]] = r[32]
		}
	}
	if got["lamd"] != "{fillfactor=70,log_autovacuum_min_duration=250}" {
		t.Errorf("pg_class.reloptions for lamd = %q, want %q", got["lamd"], "{fillfactor=70,log_autovacuum_min_duration=250}")
	}
	if got["lamdzero"] != "{log_autovacuum_min_duration=0}" {
		t.Errorf("pg_class.reloptions for lamdzero = %q, want %q", got["lamdzero"], "{log_autovacuum_min_duration=0}")
	}
	if got["lamdplain"] != "" {
		t.Errorf("pg_class.reloptions for lamdplain = %q, want \"\" (NULL)", got["lamdplain"])
	}
}

// TestLogAutovacuumMinDurationOutOfBoundsRejected verifies CREATE TABLE rejects
// an above-INT_MAX or non-integer log_autovacuum_min_duration with PG's 22023
// error. The valid range is -1–INT_MAX (a bare negative is rejected earlier by
// the parser as a syntax error, so the reachable invalid cases are overflow and
// non-integer). DU-002 slice 206.
func TestLogAutovacuumMinDurationOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"9999999999", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE lamdbad`+strconv.Itoa(i)+` (id integer) WITH (log_autovacuum_min_duration=`+v+`)`)
		if err == nil {
			t.Errorf("log_autovacuum_min_duration=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("log_autovacuum_min_duration=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("log_autovacuum_min_duration=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumFreezeMinAgeSurfacesInPgClassReloptions verifies that an integer
// autovacuum_freeze_min_age storage parameter is persisted on the table, surfaces
// in pg_class.reloptions as `autovacuum_freeze_min_age=N`, and that an explicit 0
// (the range minimum, distinct from the -1 unset sentinel) round-trips via the
// presence flag rather than a zero check. DU-002 slice 207.
func TestAutovacuumFreezeMinAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE afma (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_freeze_min_age=5000)`); err != nil {
		t.Fatalf("CREATE TABLE afma: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE afmazero (id integer PRIMARY KEY) WITH (autovacuum_freeze_min_age=0)`); err != nil {
		t.Fatalf("CREATE TABLE afmazero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE afmaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE afmaplain: %v", err)
	}

	afmaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afma"})
	if !ok {
		t.Fatal("afma table not found")
	}
	if afmaTbl.AutovacuumFreezeMinAge != 5000 || !afmaTbl.AutovacuumFreezeMinAgeSet {
		t.Errorf("afma age = %d set=%v, want 5000 set=true", afmaTbl.AutovacuumFreezeMinAge, afmaTbl.AutovacuumFreezeMinAgeSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afmazero"})
	if !ok {
		t.Fatal("afmazero table not found")
	}
	if zeroTbl.AutovacuumFreezeMinAge != 0 || !zeroTbl.AutovacuumFreezeMinAgeSet {
		t.Errorf("afmazero age = %d set=%v, want 0 set=true", zeroTbl.AutovacuumFreezeMinAge, zeroTbl.AutovacuumFreezeMinAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afmaplain"})
	if !ok {
		t.Fatal("afmaplain table not found")
	}
	if plainTbl.AutovacuumFreezeMinAgeSet {
		t.Errorf("afmaplain set=%v, want false (unset)", plainTbl.AutovacuumFreezeMinAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "afma" || r[1] == "afmazero" || r[1] == "afmaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["afma"] != "{fillfactor=70,autovacuum_freeze_min_age=5000}" {
		t.Errorf("pg_class.reloptions for afma = %q, want %q", got["afma"], "{fillfactor=70,autovacuum_freeze_min_age=5000}")
	}
	if got["afmazero"] != "{autovacuum_freeze_min_age=0}" {
		t.Errorf("pg_class.reloptions for afmazero = %q, want %q", got["afmazero"], "{autovacuum_freeze_min_age=0}")
	}
	if got["afmaplain"] != "" {
		t.Errorf("pg_class.reloptions for afmaplain = %q, want \"\" (NULL)", got["afmaplain"])
	}
}

// TestAutovacuumFreezeMinAgeOutOfBoundsRejected verifies CREATE TABLE rejects an
// above-range (> 1000000000) or non-integer autovacuum_freeze_min_age with PG's
// 22023 error. The valid range is 0–1000000000 (a bare negative is rejected
// earlier by the parser as a syntax error, so the reachable invalid cases are
// over-max and non-integer). DU-002 slice 207.
func TestAutovacuumFreezeMinAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"1000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE afmabad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_freeze_min_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_freeze_min_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_freeze_min_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_freeze_min_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumFreezeMaxAgeSurfacesInPgClassReloptions verifies that an integer
// autovacuum_freeze_max_age storage parameter is persisted on the table, surfaces
// in pg_class.reloptions as `autovacuum_freeze_max_age=N`, and that an unset table
// reports no reloption. The valid range minimum is 100000 (distinct from the -1
// unset sentinel), so presence is tracked by a flag rather than a zero check.
// DU-002 slice 208.
func TestAutovacuumFreezeMaxAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE afmx (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_freeze_max_age=500000)`); err != nil {
		t.Fatalf("CREATE TABLE afmx: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE afmxplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE afmxplain: %v", err)
	}

	afmxTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afmx"})
	if !ok {
		t.Fatal("afmx table not found")
	}
	if afmxTbl.AutovacuumFreezeMaxAge != 500000 || !afmxTbl.AutovacuumFreezeMaxAgeSet {
		t.Errorf("afmx age = %d set=%v, want 500000 set=true", afmxTbl.AutovacuumFreezeMaxAge, afmxTbl.AutovacuumFreezeMaxAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afmxplain"})
	if !ok {
		t.Fatal("afmxplain table not found")
	}
	if plainTbl.AutovacuumFreezeMaxAgeSet {
		t.Errorf("afmxplain set=%v, want false (unset)", plainTbl.AutovacuumFreezeMaxAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "afmx" || r[1] == "afmxplain") {
			got[r[1]] = r[32]
		}
	}
	if got["afmx"] != "{fillfactor=70,autovacuum_freeze_max_age=500000}" {
		t.Errorf("pg_class.reloptions for afmx = %q, want %q", got["afmx"], "{fillfactor=70,autovacuum_freeze_max_age=500000}")
	}
	if got["afmxplain"] != "" {
		t.Errorf("pg_class.reloptions for afmxplain = %q, want \"\" (NULL)", got["afmxplain"])
	}
}

// TestAutovacuumFreezeMaxAgeOutOfBoundsRejected verifies CREATE TABLE rejects a
// below-min (< 100000), above-max (> 2000000000), or non-integer
// autovacuum_freeze_max_age with PG's 22023 error. The valid range is
// 100000–2000000000. DU-002 slice 208.
func TestAutovacuumFreezeMaxAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"99999", "2000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE afmxbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_freeze_max_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_freeze_max_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_freeze_max_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_freeze_max_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumFreezeTableAgeSurfacesInPgClassReloptions verifies that an integer
// autovacuum_freeze_table_age storage parameter is persisted on the table, surfaces
// in pg_class.reloptions as `autovacuum_freeze_table_age=N`, and that an unset table
// reports no reloption. The valid range minimum is 0 (distinct from the -1 unset
// sentinel), so presence is tracked by a flag rather than a zero check.
// DU-002 slice 209.
func TestAutovacuumFreezeTableAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE afta (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_freeze_table_age=150000000)`); err != nil {
		t.Fatalf("CREATE TABLE afta: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE aftaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE aftaplain: %v", err)
	}

	aftaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "afta"})
	if !ok {
		t.Fatal("afta table not found")
	}
	if aftaTbl.AutovacuumFreezeTableAge != 150000000 || !aftaTbl.AutovacuumFreezeTableAgeSet {
		t.Errorf("afta age = %d set=%v, want 150000000 set=true", aftaTbl.AutovacuumFreezeTableAge, aftaTbl.AutovacuumFreezeTableAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "aftaplain"})
	if !ok {
		t.Fatal("aftaplain table not found")
	}
	if plainTbl.AutovacuumFreezeTableAgeSet {
		t.Errorf("aftaplain set=%v, want false (unset)", plainTbl.AutovacuumFreezeTableAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "afta" || r[1] == "aftaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["afta"] != "{fillfactor=70,autovacuum_freeze_table_age=150000000}" {
		t.Errorf("pg_class.reloptions for afta = %q, want %q", got["afta"], "{fillfactor=70,autovacuum_freeze_table_age=150000000}")
	}
	if got["aftaplain"] != "" {
		t.Errorf("pg_class.reloptions for aftaplain = %q, want \"\" (NULL)", got["aftaplain"])
	}
}

// TestAutovacuumFreezeTableAgeOutOfBoundsRejected verifies CREATE TABLE rejects an
// above-max (> 2000000000) or non-integer autovacuum_freeze_table_age with PG's
// 22023 error. The valid range is 0–2000000000; negatives (below min 0) are
// rejected earlier by the parser as a syntax error, so the reachable invalid cases
// are overflow and non-integer. DU-002 slice 209.
func TestAutovacuumFreezeTableAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"2000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE aftabad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_freeze_table_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_freeze_table_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_freeze_table_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_freeze_table_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumMultixactFreezeMinAgeSurfacesInPgClassReloptions verifies that an
// integer autovacuum_multixact_freeze_min_age storage parameter is persisted on the
// table, surfaces in pg_class.reloptions as `autovacuum_multixact_freeze_min_age=N`,
// and that an unset table reports no reloption. The valid range minimum is 0
// (distinct from the -1 unset sentinel), so presence is tracked by a flag rather
// than a zero check. DU-002 slice 210.
func TestAutovacuumMultixactFreezeMinAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE amfma (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_multixact_freeze_min_age=5000000)`); err != nil {
		t.Fatalf("CREATE TABLE amfma: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE amfmaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE amfmaplain: %v", err)
	}

	amfmaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amfma"})
	if !ok {
		t.Fatal("amfma table not found")
	}
	if amfmaTbl.AutovacuumMultixactFreezeMinAge != 5000000 || !amfmaTbl.AutovacuumMultixactFreezeMinAgeSet {
		t.Errorf("amfma age = %d set=%v, want 5000000 set=true", amfmaTbl.AutovacuumMultixactFreezeMinAge, amfmaTbl.AutovacuumMultixactFreezeMinAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amfmaplain"})
	if !ok {
		t.Fatal("amfmaplain table not found")
	}
	if plainTbl.AutovacuumMultixactFreezeMinAgeSet {
		t.Errorf("amfmaplain set=%v, want false (unset)", plainTbl.AutovacuumMultixactFreezeMinAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "amfma" || r[1] == "amfmaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["amfma"] != "{fillfactor=70,autovacuum_multixact_freeze_min_age=5000000}" {
		t.Errorf("pg_class.reloptions for amfma = %q, want %q", got["amfma"], "{fillfactor=70,autovacuum_multixact_freeze_min_age=5000000}")
	}
	if got["amfmaplain"] != "" {
		t.Errorf("pg_class.reloptions for amfmaplain = %q, want \"\" (NULL)", got["amfmaplain"])
	}
}

// TestAutovacuumMultixactFreezeMinAgeOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-max (> 1000000000) or non-integer
// autovacuum_multixact_freeze_min_age with PG's 22023 error. The valid range is
// 0–1000000000; negatives (below min 0) are rejected earlier by the parser as a
// syntax error, so the reachable invalid cases are overflow and non-integer.
// DU-002 slice 210.
func TestAutovacuumMultixactFreezeMinAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"1000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE amfmabad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_multixact_freeze_min_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_multixact_freeze_min_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_multixact_freeze_min_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_multixact_freeze_min_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumMultixactFreezeMaxAgeSurfacesInPgClassReloptions verifies that an
// integer autovacuum_multixact_freeze_max_age storage parameter is persisted on the
// table, surfaces in pg_class.reloptions as `autovacuum_multixact_freeze_max_age=N`,
// and that an unset table reports no reloption. The valid range minimum is 10000
// (distinct from the -1 unset sentinel), so presence is tracked by a flag rather
// than a zero check. DU-002 slice 211.
func TestAutovacuumMultixactFreezeMaxAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE amfmaxa (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_multixact_freeze_max_age=500000000)`); err != nil {
		t.Fatalf("CREATE TABLE amfmaxa: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE amfmaxaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE amfmaxaplain: %v", err)
	}

	amfmaxaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amfmaxa"})
	if !ok {
		t.Fatal("amfmaxa table not found")
	}
	if amfmaxaTbl.AutovacuumMultixactFreezeMaxAge != 500000000 || !amfmaxaTbl.AutovacuumMultixactFreezeMaxAgeSet {
		t.Errorf("amfmaxa age = %d set=%v, want 500000000 set=true", amfmaxaTbl.AutovacuumMultixactFreezeMaxAge, amfmaxaTbl.AutovacuumMultixactFreezeMaxAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amfmaxaplain"})
	if !ok {
		t.Fatal("amfmaxaplain table not found")
	}
	if plainTbl.AutovacuumMultixactFreezeMaxAgeSet {
		t.Errorf("amfmaxaplain set=%v, want false (unset)", plainTbl.AutovacuumMultixactFreezeMaxAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "amfmaxa" || r[1] == "amfmaxaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["amfmaxa"] != "{fillfactor=70,autovacuum_multixact_freeze_max_age=500000000}" {
		t.Errorf("pg_class.reloptions for amfmaxa = %q, want %q", got["amfmaxa"], "{fillfactor=70,autovacuum_multixact_freeze_max_age=500000000}")
	}
	if got["amfmaxaplain"] != "" {
		t.Errorf("pg_class.reloptions for amfmaxaplain = %q, want \"\" (NULL)", got["amfmaxaplain"])
	}
}

// TestAutovacuumMultixactFreezeMaxAgeOutOfBoundsRejected verifies CREATE TABLE
// rejects a below-min (< 10000), above-max (> 2000000000), or non-integer
// autovacuum_multixact_freeze_max_age with PG's 22023 error. Unlike the min/table-age
// options the lower bound is 10000, so a below-min positive value (9999) is a
// reachable invalid case alongside overflow and non-integer. DU-002 slice 211.
func TestAutovacuumMultixactFreezeMaxAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"9999", "2000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE amfmaxabad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_multixact_freeze_max_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_multixact_freeze_max_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_multixact_freeze_max_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_multixact_freeze_max_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumMultixactFreezeTableAgeSurfacesInPgClassReloptions verifies that an
// integer autovacuum_multixact_freeze_table_age storage parameter is persisted on the
// table, surfaces in pg_class.reloptions as `autovacuum_multixact_freeze_table_age=N`,
// and that an unset table reports no reloption. The valid range minimum is 0 (a valid
// explicit value distinct from the -1 unset sentinel), so presence is tracked by a flag
// rather than a zero check. DU-002 slice 212.
func TestAutovacuumMultixactFreezeTableAgeSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE amftaa (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_multixact_freeze_table_age=900000000)`); err != nil {
		t.Fatalf("CREATE TABLE amftaa: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE amftaaplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE amftaaplain: %v", err)
	}

	amftaaTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amftaa"})
	if !ok {
		t.Fatal("amftaa table not found")
	}
	if amftaaTbl.AutovacuumMultixactFreezeTableAge != 900000000 || !amftaaTbl.AutovacuumMultixactFreezeTableAgeSet {
		t.Errorf("amftaa age = %d set=%v, want 900000000 set=true", amftaaTbl.AutovacuumMultixactFreezeTableAge, amftaaTbl.AutovacuumMultixactFreezeTableAgeSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "amftaaplain"})
	if !ok {
		t.Fatal("amftaaplain table not found")
	}
	if plainTbl.AutovacuumMultixactFreezeTableAgeSet {
		t.Errorf("amftaaplain set=%v, want false (unset)", plainTbl.AutovacuumMultixactFreezeTableAgeSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "amftaa" || r[1] == "amftaaplain") {
			got[r[1]] = r[32]
		}
	}
	if got["amftaa"] != "{fillfactor=70,autovacuum_multixact_freeze_table_age=900000000}" {
		t.Errorf("pg_class.reloptions for amftaa = %q, want %q", got["amftaa"], "{fillfactor=70,autovacuum_multixact_freeze_table_age=900000000}")
	}
	if got["amftaaplain"] != "" {
		t.Errorf("pg_class.reloptions for amftaaplain = %q, want \"\" (NULL)", got["amftaaplain"])
	}
}

// TestAutovacuumMultixactFreezeTableAgeOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-max (> 2000000000) or non-integer
// autovacuum_multixact_freeze_table_age with PG's 22023 error. The lower bound is 0,
// so overflow and non-integer are the reachable invalid cases. DU-002 slice 212.
func TestAutovacuumMultixactFreezeTableAgeOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"2000000001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE amftaabad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_multixact_freeze_table_age=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_multixact_freeze_table_age=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_multixact_freeze_table_age=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_multixact_freeze_table_age=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumVacuumCostLimitSurfacesInPgClassReloptions verifies that an
// integer autovacuum_vacuum_cost_limit storage parameter is persisted on the
// table, surfaces in pg_class.reloptions as `autovacuum_vacuum_cost_limit=N`,
// and that an unset table reports no reloption. The valid range minimum is 1, so
// presence is tracked by a flag rather than a zero check. DU-002 slice 213.
func TestAutovacuumVacuumCostLimitSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE avcl (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_cost_limit=2500)`); err != nil {
		t.Fatalf("CREATE TABLE avcl: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avclplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avclplain: %v", err)
	}

	avclTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avcl"})
	if !ok {
		t.Fatal("avcl table not found")
	}
	if avclTbl.AutovacuumVacuumCostLimit != 2500 || !avclTbl.AutovacuumVacuumCostLimitSet {
		t.Errorf("avcl limit = %d set=%v, want 2500 set=true", avclTbl.AutovacuumVacuumCostLimit, avclTbl.AutovacuumVacuumCostLimitSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avclplain"})
	if !ok {
		t.Fatal("avclplain table not found")
	}
	if plainTbl.AutovacuumVacuumCostLimitSet {
		t.Errorf("avclplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumCostLimitSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "avcl" || r[1] == "avclplain") {
			got[r[1]] = r[32]
		}
	}
	if got["avcl"] != "{fillfactor=70,autovacuum_vacuum_cost_limit=2500}" {
		t.Errorf("pg_class.reloptions for avcl = %q, want %q", got["avcl"], "{fillfactor=70,autovacuum_vacuum_cost_limit=2500}")
	}
	if got["avclplain"] != "" {
		t.Errorf("pg_class.reloptions for avclplain = %q, want \"\" (NULL)", got["avclplain"])
	}
}

// TestAutovacuumVacuumCostLimitOutOfBoundsRejected verifies CREATE TABLE rejects a
// below-min (< 1, e.g. 0), above-max (> 10000) or non-integer
// autovacuum_vacuum_cost_limit with PG's 22023 error. Unlike the freeze-age options
// the lower bound is 1, so 0 is a reachable invalid case. DU-002 slice 213.
func TestAutovacuumVacuumCostLimitOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"0", "10001", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE avclbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_cost_limit=`+v+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_cost_limit=%s: expected an error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_cost_limit=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_cost_limit=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestUserCatalogTableSurfacesInPgClassReloptions verifies that a `WITH
// (user_catalog_table=BOOL)` storage parameter is persisted on the table,
// surfaces in pg_class.reloptions as `user_catalog_table=true|false`, and that
// an unset table reports no reloption. The boolean value carries no
// zero-detectable default, so presence is tracked by a flag. pg_dump renders
// the array back as `WITH (user_catalog_table='true')`. DU-002 slice 214.
func TestUserCatalogTableSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE uct (id integer PRIMARY KEY) WITH (fillfactor=70, user_catalog_table=true)`); err != nil {
		t.Fatalf("CREATE TABLE uct: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE uctf (id integer PRIMARY KEY) WITH (user_catalog_table=off)`); err != nil {
		t.Fatalf("CREATE TABLE uctf: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE uctplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE uctplain: %v", err)
	}

	uctTbl, ok := cat.LookupTable(parser.ObjectName{Name: "uct"})
	if !ok {
		t.Fatal("uct table not found")
	}
	if !uctTbl.UserCatalogTable || !uctTbl.UserCatalogTableSet {
		t.Errorf("uct value=%v set=%v, want true set=true", uctTbl.UserCatalogTable, uctTbl.UserCatalogTableSet)
	}
	uctfTbl, ok := cat.LookupTable(parser.ObjectName{Name: "uctf"})
	if !ok {
		t.Fatal("uctf table not found")
	}
	if uctfTbl.UserCatalogTable || !uctfTbl.UserCatalogTableSet {
		t.Errorf("uctf value=%v set=%v, want false set=true", uctfTbl.UserCatalogTable, uctfTbl.UserCatalogTableSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "uctplain"})
	if !ok {
		t.Fatal("uctplain table not found")
	}
	if plainTbl.UserCatalogTableSet {
		t.Errorf("uctplain set=%v, want false (unset)", plainTbl.UserCatalogTableSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "uct" || r[1] == "uctf" || r[1] == "uctplain") {
			got[r[1]] = r[32]
		}
	}
	if got["uct"] != "{fillfactor=70,user_catalog_table=true}" {
		t.Errorf("pg_class.reloptions for uct = %q, want %q", got["uct"], "{fillfactor=70,user_catalog_table=true}")
	}
	if got["uctf"] != "{user_catalog_table=false}" {
		t.Errorf("pg_class.reloptions for uctf = %q, want %q", got["uctf"], "{user_catalog_table=false}")
	}
	if got["uctplain"] != "" {
		t.Errorf("pg_class.reloptions for uctplain = %q, want \"\" (NULL)", got["uctplain"])
	}
}

// TestUserCatalogTableInvalidValueRejected verifies CREATE TABLE rejects a
// non-boolean user_catalog_table value with PG's 22023 error. DU-002 slice 214.
func TestUserCatalogTableInvalidValueRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, v := range []string{"maybe", "2", "tru3"} {
		err := runDDL(t, ctx, `CREATE TABLE uctbad`+strconv.Itoa(i)+` (id integer) WITH (user_catalog_table=`+v+`)`)
		if err == nil {
			t.Errorf("user_catalog_table=%s: expected an invalid-value error, got nil", v)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("user_catalog_table=%s: error type = %T, want *ExecError", v, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("user_catalog_table=%s: error code = %q, want 22023", v, ee.Code)
		}
	}
}

// TestAutovacuumVacuumMaxThresholdSurfacesInPgClassReloptions verifies that a
// `WITH (autovacuum_vacuum_max_threshold=N)` storage parameter declared on CREATE
// TABLE is persisted on the catalog table and surfaced through the pg_class
// virtual view's reloptions cell. Like the other INT autovacuum reloptions
// (slices 198/203/204), 0 is a valid explicit value (PG's reloption default is
// -2 = unset; the parser rejects a bare negative as a syntax error, so 0 is the
// reachable boundary). The Set flag — not a zero check — guards presence.
// Cases pin: a value alongside fillfactor (combined
// `{fillfactor=70,autovacuum_vacuum_max_threshold=5000}`), the boundary value 0
// (explicitly set), and a plain table (no reloptions). pg_dump renders
// the array back as `WITH (autovacuum_vacuum_max_threshold='5000')`. goopg has no
// autovacuum, so the value is catalog/dump-only. DU-002 slice 215.
func TestAutovacuumVacuumMaxThresholdSurfacesInPgClassReloptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE av (id integer PRIMARY KEY) WITH (fillfactor=70, autovacuum_vacuum_max_threshold=5000)`); err != nil {
		t.Fatalf("CREATE TABLE av: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avzero (id integer PRIMARY KEY) WITH (autovacuum_vacuum_max_threshold=0)`); err != nil {
		t.Fatalf("CREATE TABLE avzero: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE avplain (id integer PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE avplain: %v", err)
	}

	avTbl, ok := cat.LookupTable(parser.ObjectName{Name: "av"})
	if !ok {
		t.Fatal("av table not found")
	}
	if avTbl.AutovacuumVacuumMaxThreshold != 5000 || !avTbl.AutovacuumVacuumMaxThresholdSet {
		t.Errorf("av threshold = %d set=%v, want 5000 set=true", avTbl.AutovacuumVacuumMaxThreshold, avTbl.AutovacuumVacuumMaxThresholdSet)
	}
	zeroTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avzero"})
	if !ok {
		t.Fatal("avzero table not found")
	}
	if zeroTbl.AutovacuumVacuumMaxThreshold != 0 || !zeroTbl.AutovacuumVacuumMaxThresholdSet {
		t.Errorf("avzero threshold = %d set=%v, want 0 set=true", zeroTbl.AutovacuumVacuumMaxThreshold, zeroTbl.AutovacuumVacuumMaxThresholdSet)
	}
	plainTbl, ok := cat.LookupTable(parser.ObjectName{Name: "avplain"})
	if !ok {
		t.Fatal("avplain table not found")
	}
	if plainTbl.AutovacuumVacuumMaxThresholdSet {
		t.Errorf("avplain set=%v, want false (unset)", plainTbl.AutovacuumVacuumMaxThresholdSet)
	}

	pgClass, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_class"})
	if !ok || pgClass.VirtualRows == nil {
		t.Fatal("pg_class virtual table not found")
	}
	got := map[string]string{}
	for _, r := range pgClass.VirtualRows() {
		if len(r) > 32 && (r[1] == "av" || r[1] == "avzero" || r[1] == "avplain") {
			got[r[1]] = r[32]
		}
	}
	if got["av"] != "{fillfactor=70,autovacuum_vacuum_max_threshold=5000}" {
		t.Errorf("pg_class.reloptions for av = %q, want %q", got["av"], "{fillfactor=70,autovacuum_vacuum_max_threshold=5000}")
	}
	if got["avzero"] != "{autovacuum_vacuum_max_threshold=0}" {
		t.Errorf("pg_class.reloptions for avzero = %q, want %q", got["avzero"], "{autovacuum_vacuum_max_threshold=0}")
	}
	if got["avplain"] != "" {
		t.Errorf("pg_class.reloptions for avplain = %q, want \"\" (NULL)", got["avplain"])
	}
}

// TestAutovacuumVacuumMaxThresholdOutOfBoundsRejected verifies CREATE TABLE
// rejects an above-INT_MAX or non-integer autovacuum_vacuum_max_threshold with
// PG's 22023 error. The valid range is -1–INT_MAX (a bare negative below -1 is
// rejected earlier by the parser as a syntax error, so the reachable invalid
// cases are overflow and non-integer). DU-002 slice 215.
func TestAutovacuumVacuumMaxThresholdOutOfBoundsRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for i, av := range []string{"9999999999", "nope"} {
		err := runDDL(t, ctx, `CREATE TABLE avmtbad`+strconv.Itoa(i)+` (id integer) WITH (autovacuum_vacuum_max_threshold=`+av+`)`)
		if err == nil {
			t.Errorf("autovacuum_vacuum_max_threshold=%s: expected an error, got nil", av)
			continue
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("autovacuum_vacuum_max_threshold=%s: error type = %T, want *ExecError", av, err)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("autovacuum_vacuum_max_threshold=%s: error code = %q, want 22023", av, ee.Code)
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
