package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// functionCatalog is a small test helper mirroring eventTriggerCatalog: the
// Runtime.Catalog field is the catalog.Catalog interface, but Routines()
// lives on the concrete *catalog.InMemory implementation.
func functionCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestFunctionDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004, loop #71 ledger resume point): a
// CREATE FUNCTION WAL record written by a pre-crash run is replayed into
// the catalog's Routines registry on the post-crash Open path, so pg_proc
// (pg_dump's function dump, and any event trigger's evtfoid reference)
// round-trips after restart even though the goopg process never wrote a
// JSON snapshot. OID/schema/args/return type/body/attributes all survive.
func TestFunctionDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(200000)
	payload := wal.CreateFunctionPayload{
		OID: wantOID, Schema: "public", Name: "myfunc",
		Args: []wal.FunctionArgPayload{
			{Name: "a", TypeName: "integer", Mode: "i"},
			{Name: "b", TypeName: "numeric", TypeArgs: []int64{10, 2}, Mode: "i"},
		},
		ReturnTypeName: "integer",
		Language:       "sql", Body: "select $1 + $2",
		Volatile: "i", KindChar: "f",
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateFunction(payload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-function: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the function without any JSON
	// snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	rs := functionCatalog(t, rt2).Routines()
	r := rs.LookupByOID(wantOID)
	if r == nil {
		t.Fatalf("after WAL replay, function OID %d not found", wantOID)
	}
	if r.Name != "myfunc" || r.Schema != "public" || r.Language != "sql" || r.Body != "select $1 + $2" || r.Volatile != "i" {
		t.Errorf("after WAL replay, routine = %+v, want Name=myfunc Schema=public Language=sql Volatile=i", r)
	}
	if len(r.ArgTypes) != 2 || r.ArgTypes[0].Name != "integer" || r.ArgTypes[1].Name != "numeric" {
		t.Errorf("after WAL replay, ArgTypes = %+v", r.ArgTypes)
	}
	if len(r.ArgTypes) == 2 && (len(r.ArgTypes[1].Args) != 2 || r.ArgTypes[1].Args[0] != 10 || r.ArgTypes[1].Args[1] != 2) {
		t.Errorf("after WAL replay, ArgTypes[1].Args = %v, want [10 2] (numeric(10,2) precision/scale)", r.ArgTypes[1].Args)
	}

	// A later CREATE FUNCTION statement must not collide with the
	// replayed OID's own allocation sequence.
	next, cerr := rs.Create(&catalog.Routine{Schema: "public", Name: "another", Volatile: "v"}, false)
	if cerr != nil {
		t.Fatalf("Create after replay: %v", cerr)
	}
	if next.OID <= wantOID {
		t.Errorf("post-replay OID allocation = %d, want > %d (nextOID must advance past the replayed OID)", next.OID, wantOID)
	}
}

// TestFunctionDDLRecoveryReplaysCreateOrReplace confirms a CREATE OR
// REPLACE record following its own earlier CREATE overwrites in place
// (same schema.name(sig) key, same OID) rather than duplicating the
// registry entry.
func TestFunctionDDLRecoveryReplaysCreateOrReplace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(200010)
	first := wal.CreateFunctionPayload{
		OID: wantOID, Schema: "public", Name: "replaceme",
		ReturnTypeName: "integer", Language: "sql", Body: "select 1",
		Volatile: "v", KindChar: "f",
	}
	second := first
	second.Body = "select 2"
	second.Volatile = "i"
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateFunction(first)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append first create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateFunction(second)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append or-replace: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	rs := functionCatalog(t, rt2).Routines()
	all := rs.LookupByName(parser.ObjectName{Schema: "public", Name: "replaceme"})
	if len(all) != 1 {
		t.Fatalf("after CREATE OR REPLACE replay, %d overloads found, want 1: %+v", len(all), all)
	}
	if all[0].OID != wantOID || all[0].Body != "select 2" || all[0].Volatile != "i" {
		t.Errorf("after CREATE OR REPLACE replay, routine = %+v, want OID=%d Body=\"select 2\" Volatile=i", all[0], wantOID)
	}
}

// TestFunctionDDLRecoveryReplaysDropAfterCreate confirms a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestFunctionDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(200020)
	payload := wal.CreateFunctionPayload{
		OID: wantOID, Schema: "public", Name: "dropme",
		ReturnTypeName: "integer", Language: "sql", Body: "select 1",
		Volatile: "v", KindChar: "f",
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateFunction(payload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropFunction(wantOID)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append drop: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	if r := functionCatalog(t, rt2).Routines().LookupByOID(wantOID); r != nil {
		t.Errorf("after CREATE + DROP replay, function OID %d = %+v, want not found", wantOID, r)
	}
}

// TestFunctionDDLRecoveryReplaysAlterAfterCreate confirms RENAME and the
// four mutable attributes both replay in order on top of the CREATE
// record, without disturbing the OID.
func TestFunctionDDLRecoveryReplaysAlterAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(200030)
	payload := wal.CreateFunctionPayload{
		OID: wantOID, Schema: "public", Name: "origfunc",
		ReturnTypeName: "integer", Language: "sql", Body: "select 1",
		Volatile: "v", KindChar: "f",
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateFunction(payload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterFunctionRename(wantOID, "renamedfunc")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append rename: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterFunctionFlags(wantOID, "i", true, true, true)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append flags: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterFunctionOwner(wantOID, 16400)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append owner: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeAlterFunctionSetSchema(wantOID, "app")); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append set-schema: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	rs := functionCatalog(t, rt2).Routines()
	r := rs.LookupByOID(wantOID)
	if r == nil {
		t.Fatalf("after CREATE+RENAME+FLAGS+OWNER+SET SCHEMA replay, OID %d not found", wantOID)
	}
	if r.Name != "renamedfunc" {
		t.Errorf("after replay, Name = %q, want \"renamedfunc\"", r.Name)
	}
	if r.Volatile != "i" || !r.SecurityDefiner || !r.Leakproof || !r.Strict {
		t.Errorf("after replay, attrs = Volatile=%q SecurityDefiner=%v Leakproof=%v Strict=%v, want i/true/true/true",
			r.Volatile, r.SecurityDefiner, r.Leakproof, r.Strict)
	}
	if r.Owner != 16400 {
		t.Errorf("after replay, Owner = %d, want 16400", r.Owner)
	}
	if r.Schema != "app" {
		t.Errorf("after replay, Schema = %q, want \"app\"", r.Schema)
	}
	if rs.LookupByOID(wantOID) != r {
		t.Fatal("LookupByOID should still resolve the same routine pointer after SET SCHEMA re-keying")
	}
}

// TestReplayFunctionDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayFunctionDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayFunctionDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
}

// TestReplayFunctionDDLRecordsHandlesNilCatalog verifies the recovery hook
// tolerates a nil catalog (mirrors the nil-registry guard the event
// trigger DDL recovery driver has for embedded test setups).
func TestReplayFunctionDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayFunctionDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
