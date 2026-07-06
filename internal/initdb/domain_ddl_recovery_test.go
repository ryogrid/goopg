package initdb

import (
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// domainCatalog is a small test helper mirroring rangeTypeCatalog: the
// Runtime.Catalog field is the catalog.Catalog interface, but the domain
// registry lives on the concrete *catalog.InMemory implementation.
func domainCatalog(t *testing.T, rt *Runtime) *catalog.InMemory {
	t.Helper()
	im, ok := rt.Catalog.(*catalog.InMemory)
	if !ok {
		t.Fatalf("Runtime.Catalog is not *catalog.InMemory: %T", rt.Catalog)
	}
	return im
}

// TestDomainDDLRecoveryReplaysCreate confirms the M0122-0005
// restart-persistence follow-up (deferral ledger 2026-07-06 row: "domains
// have no restart persistence at all"): a CREATE DOMAIN WAL record written by
// a pre-crash run is replayed into the catalog's domains registry on the
// post-crash Open path, including its DEFAULT expression and CHECK
// constraints, so pg_type/pg_constraint (pg_dump's dumpDomain) round-trips
// after restart even though the goopg process never wrote a JSON snapshot.
func TestDomainDDLRecoveryReplaysCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	payload := wal.CreateDomainPayload{
		Name: "us_zip", OID: 40900, ArrayOID: 40901,
		BaseName: "varchar", BaseArgs: []int64{10},
		// DefaultSQL is the raw expression AST as written in the DEFAULT
		// clause (catalog.FormatExprForAttrdef output, mirroring
		// RecordKindColumnDefaults) — no domain-specific `::type` decoration,
		// which Domain.DefaultBin() adds dynamically at render time from
		// Base.Name instead.
		NotNull: true, Owner: 16400, DefaultSQL: "'00000'",
		Checks: []wal.DomainCheckPayload{
			{OID: 40902, Name: "us_zip_check", Expr: "(length((VALUE)::text) = 5)"},
		},
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateDomain(payload)); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create-domain: %v", werr)
	}
	if ferr := rt1.WAL.FlushUpTo(rt1.WAL.WrittenLSN()); ferr != nil {
		_ = rt1.Close()
		t.Fatalf("FlushUpTo: %v", ferr)
	}
	if err := rt1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second Open: WAL replay must surface the domain without any JSON
	// snapshot help.
	rt2, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer rt2.Close()

	found, ok := domainCatalog(t, rt2).LookupDomain("us_zip")
	if !ok {
		t.Fatalf("after WAL replay, domain \"us_zip\" not found")
	}
	if found.OID != payload.OID || found.ArrayOID != payload.ArrayOID || found.Base.Name != "varchar" ||
		len(found.Base.Args) != 1 || found.Base.Args[0] != 10 || !found.NotNull || found.Owner != payload.Owner {
		t.Fatalf("after WAL replay, domain = %+v, want OID=%d ArrayOID=%d Base.Name=varchar Base.Args=[10] NotNull=true Owner=%d",
			found, payload.OID, payload.ArrayOID, payload.Owner)
	}
	if found.DefaultBin() == "" {
		t.Errorf("after WAL replay, DefaultBin() is empty, want a reconstructed DEFAULT expression")
	}
	if len(found.Checks) != 1 || found.Checks[0].Name != "us_zip_check" || found.Checks[0].OID != 40902 {
		t.Errorf("after WAL replay, Checks = %+v, want one us_zip_check (OID 40902)", found.Checks)
	}
}

// TestDomainDDLRecoveryReplaysDropAfterCreate confirms a CREATE followed by a
// DROP cancels out — the registry agrees with the most recent durable
// record, not the first one.
func TestDomainDDLRecoveryReplaysDropAfterCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	rt1, err := Open(OpenOptions{DataDir: dir, PoolSlots: 4})
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeCreateDomain(wal.CreateDomainPayload{
		Name: "posint", OID: 40910, ArrayOID: 40911, BaseName: "int4", Owner: 10,
	})); werr != nil {
		_ = rt1.Close()
		t.Fatalf("WAL.Append create: %v", werr)
	}
	if _, _, werr := rt1.WAL.Append(wal.EncodeDropDomain("posint")); werr != nil {
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

	if found, ok := domainCatalog(t, rt2).LookupDomain("posint"); ok {
		t.Errorf("after CREATE + DROP replay, domain \"posint\" = %+v, want not found", found)
	}
}

// TestReplayDomainDDLRecordsHandlesMissingWalDir verifies the recovery hook
// is idempotent when invoked against a missing pg_wal directory (brand new
// initdb).
func TestReplayDomainDDLRecordsHandlesMissingWalDir(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := replayDomainDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), cat); err != nil {
		t.Fatalf("replay against missing dir: %v", err)
	}
}

// TestReplayDomainDDLRecordsHandlesNilCatalog verifies the recovery hook
// tolerates a nil catalog (mirrors the nil-registry guard the range type DDL
// recovery driver has for embedded test setups).
func TestReplayDomainDDLRecordsHandlesNilCatalog(t *testing.T) {
	if err := replayDomainDDLRecords(filepath.Join(t.TempDir(), "nonexistent"), nil); err != nil {
		t.Fatalf("replay with nil catalog: %v", err)
	}
}
