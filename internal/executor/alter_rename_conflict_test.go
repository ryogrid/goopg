package executor

import (
	"strings"
	"testing"
)

// TestAlterCollationRenameConflictIsDuplicateObject pins review/260831-2
// EO1-7: execAlterCollation's rename arm mapped EVERY RenameCollation error
// to notFound(), so renaming onto a name that is already taken reported
// 42704 "does not exist" instead of PG's 42710 duplicate_object — and with
// IF EXISTS it was downgraded to a NOTICE and reported success while nothing
// had been renamed. PG 18.3 oracle: ERROR "collation \"zzc2\" for encoding
// \"UTF8\" already exists in schema \"public\"".
func TestAlterCollationRenameConflictIsDuplicateObject(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, name := range []string{"cc1", "cc2"} {
		if err := runDDL(t, ctx, `CREATE COLLATION `+name+` (LOCALE = 'C')`); err != nil {
			t.Fatalf("CREATE COLLATION %s: %v", name, err)
		}
	}

	err := runDDL(t, ctx, `ALTER COLLATION cc1 RENAME TO cc2`)
	if err == nil {
		t.Fatal("rename onto a taken collation name should error")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42710" {
		t.Fatalf("err = %v, want *ExecError{Code: 42710}", err)
	}
	want := `collation "cc2" for encoding "UTF8" already exists in schema "public"`
	if ee.Message != want {
		t.Errorf("message = %q, want %q", ee.Message, want)
	}

	// IF EXISTS must not swallow the collision: the source collation DOES
	// exist, so the conflict is still an error and cc1 must survive.
	err = runDDL(t, ctx, `ALTER COLLATION IF EXISTS cc1 RENAME TO cc2`)
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42710" {
		t.Fatalf("IF EXISTS rename collision: err = %v, want *ExecError{Code: 42710}", err)
	}
	if err := runDDL(t, ctx, `ALTER COLLATION cc1 RENAME TO cc3`); err != nil {
		t.Fatalf("cc1 should still exist after the failed renames: %v", err)
	}
}

// TestAlterConversionRenameConflictIsDuplicateObject is the conversion twin
// of the collation case above — execAlterConversion carried the identical
// notFound()-for-every-error mapping. PG 18.3 oracle: ERROR "conversion
// \"zzcv2\" already exists in schema \"public\"" (alter.c
// report_namespace_conflict).
func TestAlterConversionRenameConflictIsDuplicateObject(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, name := range []string{"cv1", "cv2"} {
		if err := runDDL(t, ctx, `CREATE CONVERSION `+name+` FOR 'LATIN1' TO 'UTF8' FROM iso8859_1_to_utf8`); err != nil {
			t.Fatalf("CREATE CONVERSION %s: %v", name, err)
		}
	}

	err := runDDL(t, ctx, `ALTER CONVERSION cv1 RENAME TO cv2`)
	if err == nil {
		t.Fatal("rename onto a taken conversion name should error")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "42710" {
		t.Fatalf("err = %v, want *ExecError{Code: 42710}", err)
	}
	want := `conversion "cv2" already exists in schema "public"`
	if ee.Message != want {
		t.Errorf("message = %q, want %q", ee.Message, want)
	}
	if !strings.Contains(err.Error(), "cv2") {
		t.Errorf("err = %v, want it to name the conflicting conversion", err)
	}
}
