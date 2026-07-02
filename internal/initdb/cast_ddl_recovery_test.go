package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// TestCastDDLRecoveryReplaysCreate confirms the DU-002 restart-persistence
// hook (M0119-0004 follow-up): a CREATE CAST WAL record written by a
// pre-crash run is replayed into the catalog's cast registry on the
// post-crash Open path, so pg_cast (pg_dump's getCasts/dumpCast) round-trips
// after restart even though the goopg process never wrote a JSON snapshot.
// The OID and resolved function OID are preserved across the restart.
func TestCastDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40456)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCast("integer", "text", "a", "f", wantOID, 131072)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-cast: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the cast in the registry without
	// any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	cs := cat.CastByTypes("integer", "text")
	if cs == nil {
		t.Fatalf("after WAL replay, CastByTypes(integer, text) = nil")
	}
	casts := cat.ListCasts()
	if len(casts) != 1 {
		t.Fatalf("after WAL replay, ListCasts() = %d entries, want 1", len(casts))
	}
	if cs.OID != wantOID || cs.FuncOID != 131072 || cs.Context != "a" || cs.Method != "f" {
		t.Errorf("after WAL replay, cast = %+v, want OID=%d FuncOID=131072 Context=a Method=f", cs, wantOID)
	}
}

// TestCastDDLRecoveryReplaysDropAfterCreate confirms that a CREATE followed
// by a DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestCastDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateCast("integer", "text", "a", "f", 40500, 131072)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropCast("integer", "text")); werr != nil {
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

	cat := rt2.Catalog.(*catalog.InMemory)
	if cs := cat.CastByTypes("integer", "text"); cs != nil {
		t.Errorf("after CREATE + DROP replay, CastByTypes(integer, text) = %+v, want nil", cs)
	}
}

// TestReplayCastDDLRecordsHandlesMissingWalDir verifies the recovery hook is
// idempotent when invoked against a missing pg_wal directory (brand new
// initdb).
func TestReplayCastDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayCastDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if cs := cat.CastByTypes("integer", "text"); cs != nil {
		t.Errorf("no-op replay should not register any cast, got %+v", cs)
	}
}
