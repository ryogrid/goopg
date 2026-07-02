package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// findUserConversion looks up a user conversion by (schema, name) in the
// registry snapshot returned by ListUserConversions, resolving schema via the
// catalog's schema-name map (mirrors CreateConversion's own resolution).
func findUserConversion(cat *catalog.InMemory, name, schema string) *catalog.UserConversion {
	nsOID := cat.SchemaOID(schema)
	for _, uc := range cat.ListUserConversions() {
		if uc.Name == name && uc.NamespaceOID == nsOID {
			return uc
		}
	}
	return nil
}

// TestConversionDDLRecoveryReplaysCreate confirms the DU-002
// restart-persistence hook (M0119-0004 follow-up): a CREATE CONVERSION WAL
// record written by a pre-crash run is replayed into the catalog's
// conversion registry on the post-crash Open path, so pg_conversion
// (pg_dump's getConversions/dumpConversion) round-trips after restart even
// though the goopg process never wrote a JSON snapshot. The OID and resolved
// function OID are preserved across the restart, and NamespaceOID is
// re-resolved from the schema name against the post-restart schema map.
func TestConversionDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	const wantOID = uint32(40460)
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateConversion("myconv", "public", "pg_catalog", "utf8_to_ascii", wantOID, 10, 1811, 6, 0, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-conversion: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the conversion in the registry
	// without any JSON snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	cat, ok := rt2.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Catalog is %T, expected *catalog.InMemory", rt2.Catalog)
	}
	uc := findUserConversion(cat, "myconv", "public")
	if uc == nil {
		t.Fatalf("after WAL replay, conversion \"myconv\" not found; registry = %+v", cat.ListUserConversions())
	}
	if uc.OID != wantOID || uc.FuncOID != 1811 || uc.ForEncoding != 6 || uc.ToEncoding != 0 || uc.Default {
		t.Errorf("after WAL replay, conversion = %+v, want OID=%d FuncOID=1811 ForEncoding=6 ToEncoding=0 Default=false", uc, wantOID)
	}
}

// TestConversionDDLRecoveryReplaysDropAfterCreate confirms that a CREATE
// followed by a DROP cancels out — the registry agrees with the most recent
// durable record, not the first one.
func TestConversionDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateConversion("myconv", "public", "pg_catalog", "utf8_to_ascii", 40500, 10, 1811, 6, 0, false)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropConversion("myconv", "public")); werr != nil {
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
	if uc := findUserConversion(cat, "myconv", "public"); uc != nil {
		t.Errorf("after CREATE + DROP replay, conversion \"myconv\" = %+v, want nil", uc)
	}
}

// TestReplayConversionDDLRecordsHandlesMissingWalDir verifies the recovery
// hook is idempotent when invoked against a missing pg_wal directory (brand
// new initdb).
func TestReplayConversionDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayConversionDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
	if len(cat.ListUserConversions()) != 0 {
		t.Errorf("no-op replay should not register any conversion, got %+v", cat.ListUserConversions())
	}
}
