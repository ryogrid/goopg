package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/utils/activity"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPgStatSslView confirms pg_stat_ssl is registered with the exact PG 18.3
// column shape and emits one all-false/NULL row per live *client* backend
// (client_port IS NOT NULL filter drops background workers), since goopg has
// no TLS.
func TestPgStatSslView(t *testing.T) {
	cat := catalog.NewInMemory()
	reg := activity.NewRegistry()
	if err := registerPgStatSslView(cat, reg); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_ssl"})
	if !ok {
		t.Fatal("pg_stat_ssl not registered")
	}

	wantCols := []string{"pid", "ssl", "version", "cipher", "bits", "client_dn", "client_serial", "issuer_dn"}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("column count = %d, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, c := range wantCols {
		if tbl.Columns[i].Name != c {
			t.Errorf("col[%d] = %q, want %q", i, tbl.Columns[i].Name, c)
		}
	}

	if got := tbl.VirtualRows(); len(got) != 0 {
		t.Fatalf("empty registry must yield 0 rows, got %d", len(got))
	}

	// Client backend (client_port set) → one row; background worker
	// (client_port empty) → filtered out.
	reg.Register(&activity.Backend{PID: "101", ClientPort: "54321", BackendType: "client_backend"})
	reg.Register(&activity.Backend{PID: "102", ClientPort: "", BackendType: "checkpointer"})

	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (client backend only)", len(rows))
	}
	row := rows[0]
	want := []string{"101", "f", "", "", "", "", "", ""}
	for i := range want {
		if row[i] != want[i] {
			t.Errorf("%s = %q, want %q", wantCols[i], row[i], want[i])
		}
	}
}

// TestPgStatGssapiView confirms pg_stat_gssapi is registered with the exact PG
// 18.3 column shape and emits one all-false/NULL row per live client backend,
// since goopg has no GSSAPI.
func TestPgStatGssapiView(t *testing.T) {
	cat := catalog.NewInMemory()
	reg := activity.NewRegistry()
	if err := registerPgStatGssapiView(cat, reg); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_gssapi"})
	if !ok {
		t.Fatal("pg_stat_gssapi not registered")
	}

	wantCols := []string{"pid", "gss_authenticated", "principal", "encrypted", "credentials_delegated"}
	if len(tbl.Columns) != len(wantCols) {
		t.Fatalf("column count = %d, want %d", len(tbl.Columns), len(wantCols))
	}
	for i, c := range wantCols {
		if tbl.Columns[i].Name != c {
			t.Errorf("col[%d] = %q, want %q", i, tbl.Columns[i].Name, c)
		}
	}

	if got := tbl.VirtualRows(); len(got) != 0 {
		t.Fatalf("empty registry must yield 0 rows, got %d", len(got))
	}

	reg.Register(&activity.Backend{PID: "201", ClientPort: "54322", BackendType: "client_backend"})
	reg.Register(&activity.Backend{PID: "202", ClientPort: "", BackendType: "walwriter"})

	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (client backend only)", len(rows))
	}
	row := rows[0]
	want := []string{"201", "f", "", "f", "f"}
	for i := range want {
		if row[i] != want[i] {
			t.Errorf("%s = %q, want %q", wantCols[i], row[i], want[i])
		}
	}
}
