package initdb

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

func TestLoadUserTablesFromPhysicalPGCatalogTuples(t *testing.T) {
	dataDir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer mgr.Close()

	dbOid := uint32(5)
	relOID := catalog.FirstUserOID + 1
	relFileNodeOID := relOID + 99
	writeRawCatalogPage(t, mgr, storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.RelationRelationId,
		Fork:   storage.MainFork,
	}, mustRawHeapTuple(t, encodePhysicalPGClassData(catalog.PGClassRow{
		OID:            relOID,
		RelName:        "bench_log",
		RelNamespace:   catalog.PublicNamespaceOID,
		RelKind:        "r",
		RelNAtts:       2,
		RelFileNode:    relFileNodeOID,
		RelPersistence: "p",
		RelIsShared:    false,
	})))

	writeRawCatalogPage(t, mgr, storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.AttributeRelationId,
		Fork:   storage.MainFork,
	},
		mustRawHeapTuple(t, encodePhysicalPGAttributeData(catalog.PGAttributeRow{
			AttRelID:     relOID,
			AttName:      "tableoid",
			AttTypID:     catalog.OIDOID,
			AttNum:       -6,
			AttNotNull:   true,
			AttIsDropped: false,
		})),
		mustRawHeapTuple(t, encodePhysicalPGAttributeData(catalog.PGAttributeRow{
			AttRelID:     relOID,
			AttName:      "client",
			AttTypID:     catalog.OIDInt4,
			AttNum:       1,
			AttNotNull:   true,
			AttIsDropped: false,
		})),
		mustRawHeapTuple(t, encodePhysicalPGAttributeData(catalog.PGAttributeRow{
			AttRelID:     relOID,
			AttName:      "message",
			AttTypID:     catalog.OIDText,
			AttNum:       2,
			AttNotNull:   false,
			AttIsDropped: false,
		})),
	)

	cat := catalog.NewInMemory()
	cat.SetDBOID(dbOid)
	if err := os.MkdirAll(filepath.Join(dataDir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	clog, err := mvcc.OpenCLog(filepath.Join(dataDir, "global", "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	if err := clog.EnablePGSLRUMirror(filepath.Join(dataDir, "pg_xact")); err != nil {
		t.Fatal(err)
	}
	if err := loadUserTablesFromHeap(mgr, cat, clog); err != nil {
		t.Fatal(err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "public", Name: "bench_log"})
	if !ok {
		t.Fatal("bench_log not found after physical catalog heap load")
	}
	if _, ok := cat.LookupTable(parser.ObjectName{Name: "bench_log"}); !ok {
		t.Fatal("bench_log not found via unqualified public lookup")
	}
	if got := cat.RelFileNode(tbl).RelOid; got != relFileNodeOID {
		t.Fatalf("RelFileNode relOid = %d, want %d", got, relFileNodeOID)
	}
	if len(tbl.Columns) != 2 {
		t.Fatalf("bench_log has %d columns, want 2", len(tbl.Columns))
	}
	if got := tbl.Columns[0].Name; got != "client" {
		t.Fatalf("col[0].name = %q, want client", got)
	}
	if got := tbl.Columns[0].Type.Name; got != "int4" {
		t.Fatalf("col[0].type = %q, want int4", got)
	}
	if !tbl.Columns[0].NotNull {
		t.Fatal("col[0].notnull = false, want true")
	}
	if got := tbl.Columns[1].Name; got != "message" {
		t.Fatalf("col[1].name = %q, want message", got)
	}
	if got := tbl.Columns[1].Type.Name; got != "text" {
		t.Fatalf("col[1].type = %q, want text", got)
	}
	if tbl.Columns[1].NotNull {
		t.Fatal("col[1].notnull = true, want false")
	}
}

func TestDetectCatalogDBOIDReadsPhysicalPGDatabase(t *testing.T) {
	dataDir := t.TempDir()
	globalDir := filepath.Join(dataDir, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		oid  uint32
		name string
	}{
		{oid: 1, name: "template1"},
		{oid: 5, name: "postgres"},
	} {
		raw := mustRawHeapTuple(t, encodePhysicalPGDatabaseData(row.oid, row.name))
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(globalDir, "1262"), page, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := detectCatalogDBOID(dataDir); got != 5 {
		t.Fatalf("detectCatalogDBOID = %d, want 5", got)
	}
}

func writeRawCatalogPage(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, raws ...[]byte) {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(page); err != nil {
		t.Fatal(err)
	}
	for _, raw := range raws {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.Extend(rel, page); err != nil {
		t.Fatal(err)
	}
}

func mustRawHeapTuple(t *testing.T, data []byte) []byte {
	t.Helper()
	raw, err := storage.NewHeapTuple(1, storage.InvalidTransactionID, data).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func encodePhysicalPGClassData(r catalog.PGClassRow) []byte {
	data := make([]byte, 144)
	binary.LittleEndian.PutUint32(data[0:4], r.OID)
	encodePGName(data[4:68], r.RelName)
	binary.LittleEndian.PutUint32(data[68:72], r.RelNamespace)
	binary.LittleEndian.PutUint32(data[88:92], r.RelFileNode)
	if r.RelIsShared {
		data[117] = 1
	}
	if len(r.RelPersistence) > 0 {
		data[118] = r.RelPersistence[0]
	}
	if len(r.RelKind) > 0 {
		data[119] = r.RelKind[0]
	}
	binary.LittleEndian.PutUint16(data[120:122], uint16(int16(r.RelNAtts)))
	return data
}

func encodePhysicalPGAttributeData(r catalog.PGAttributeRow) []byte {
	data := make([]byte, 100)
	binary.LittleEndian.PutUint32(data[0:4], r.AttRelID)
	encodePGName(data[4:68], r.AttName)
	binary.LittleEndian.PutUint32(data[68:72], r.AttTypID)
	binary.LittleEndian.PutUint16(data[74:76], uint16(int16(r.AttNum)))
	if r.AttNotNull {
		data[86] = 1
	}
	if r.AttIsDropped {
		data[91] = 1
	}
	return data
}

func encodePhysicalPGDatabaseData(oid uint32, name string) []byte {
	data := make([]byte, 96)
	binary.LittleEndian.PutUint32(data[0:4], oid)
	encodePGName(data[4:68], name)
	return data
}

func encodePGName(dst []byte, s string) {
	copy(dst, []byte(s))
}
