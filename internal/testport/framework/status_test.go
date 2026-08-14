package framework

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidateStatus(t *testing.T) {
	csv := `id,suite_id,kind,item_path,status,pass_required,deferred_to,rationale
P-001,client-tools-tap,tap,postgres/src/bin/pg_ctl/t/001_start_stop.pl,port,yes,-,Ported as TestPort_PgCtl001StartStop
,regress-sql,regress,postgres/src/test/regress/sql/boolean.sql,pass,yes,-,Confirmed pass via TestPort_RegressSuite
D-002,isolation-specs,isolation,postgres/src/test/isolation/specs/x.spec,defer,no,M0060-0004,Needs scheduler
,modules-suites,mixed,postgres/src/test/modules/Makefile,excluded,no,-,Out of scope
`
	path := filepath.Join(t.TempDir(), "inventory.csv")
	if err := os.WriteFile(path, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadStatusCSV(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows=%d want 4", len(rows))
	}
	if rows[0].ID != "P-001" || rows[0].Kind != "tap" || rows[0].Status != "port" || rows[0].PassRequired != "yes" {
		t.Fatalf("row 0 misparsed: %+v", rows[0])
	}
	if rows[1].ID != "" || rows[1].SuiteID != "regress-sql" || rows[1].Status != "pass" {
		t.Fatalf("row 1 misparsed: %+v", rows[1])
	}
	if err := ValidateStatusRows(rows); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsDuplicateID(t *testing.T) {
	rows := []StatusRow{
		{ID: "X", SuiteID: "client-tools-tap", Kind: "tap", ItemPath: "postgres/src/a.pl", Status: "port", PassRequired: "yes", Rationale: "TestPort_A"},
		{ID: "X", SuiteID: "client-tools-tap", Kind: "tap", ItemPath: "postgres/src/b.pl", Status: "port", PassRequired: "yes", Rationale: "TestPort_B"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected duplicate ID validation error")
	}
}

func TestValidateRejectsExcludedMustPass(t *testing.T) {
	rows := []StatusRow{
		{SuiteID: "client-tools-tap", Kind: "tap", ItemPath: "postgres/src/a.pl", Status: "excluded", PassRequired: "yes", Rationale: "r"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected excluded+pass_required=yes to be rejected")
	}
}

func TestValidateRejectsBadVocabulary(t *testing.T) {
	rows := []StatusRow{
		{SuiteID: "client-tools-tap", Kind: "tap", ItemPath: "postgres/src/a.pl", Status: "bogus", PassRequired: "no", Rationale: "r"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected unsupported status to be rejected")
	}
}

func TestValidateRejectsDeferWithoutDeferredTo(t *testing.T) {
	rows := []StatusRow{
		{SuiteID: "isolation-specs", Kind: "isolation", ItemPath: "postgres/src/test/isolation/specs/x.spec", Status: "defer", PassRequired: "no", Rationale: "r"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected defer without deferred_to to be rejected")
	}
}

func TestValidateRejectsPortWithoutTestFunc(t *testing.T) {
	rows := []StatusRow{
		{SuiteID: "client-tools-tap", Kind: "tap", ItemPath: "postgres/src/a.pl", Status: "port", PassRequired: "yes", Rationale: "no func name here"},
	}
	if err := ValidateStatusRows(rows); err == nil {
		t.Fatalf("expected port without TestPort func to be rejected")
	}
}
