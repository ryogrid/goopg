package framework

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidateAndRenderStatus(t *testing.T) {
	csv := `id,upstream_path,suite_type,status,pass_required,rationale,deferred_to
D-001,postgres/src/test/regress,regress,defer,no,Needs regress harness,M0060-0002
P-001,postgres/src/bin/pg_ctl/t/001_start_stop.pl,tap,port,yes,Ported in go test,M0060-0003
E-001,postgres/src/test/modules/unsafe_tests,modules,excluded,no,Unsafe by policy,-
`
	path := filepath.Join(t.TempDir(), "status.csv")
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadStatusCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3", len(rows))
	}
	if err := ValidateStatusRows(rows); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := WriteStatusMarkdown(&b, rows); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "# PostgreSQL Oracle Test-Port Status") {
		t.Fatalf("missing header")
	}
	if !strings.Contains(out, "| regress |") {
		t.Fatalf("missing suite summary row")
	}
}

func TestValidateRejectsDuplicateID(t *testing.T) {
	rows := []StatusRow{
		{ID: "X", UpstreamPath: "postgres/src/test/regress", SuiteType: "regress", Status: "defer", PassRequired: "no", Rationale: "r1", DeferredTo: "M1"},
		{ID: "X", UpstreamPath: "postgres/src/test/isolation", SuiteType: "isolation", Status: "defer", PassRequired: "no", Rationale: "r2", DeferredTo: "M2"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected duplicate ID validation error")
	}
}
