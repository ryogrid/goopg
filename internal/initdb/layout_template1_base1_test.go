package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestTemplate1MigrationIsRestartable pins M0149-0001a1 S3's crash-safety
// rules for moving template1 from base/2 to base/1:
//   - prepare persists the legacy WAL end and reports the migration pending;
//   - the copy replaces base/1 (the stale pre-S1 postgres copy) with base/2
//     and restores an emptied bootstrap catalog from template0 (base/4);
//   - a restart inside the window (feature not yet written) never copies
//     again, so changes made to base/1 after the copy survive;
//   - finish writes the feature and removes base/2 and the copy marker, and
//     the next prepare retires the legacy WAL end marker.
func TestTemplate1MigrationIsRestartable(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(rel string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	write("global/pg_goopg_features", []byte(PostgresCatalogsAtBase5Feature+"\n"))
	write("base/1/1259", []byte("stale-postgres"))
	write("base/1/9999", []byte("stale-only-in-base1"))
	write("base/2/1259", []byte("template1-pg_class"))
	write("base/2/2615", nil) // emptied bootstrap catalog
	write("base/2/16400", []byte("template1-user-table"))
	write("base/4/2615", []byte("pristine-pg_namespace"))
	if err := os.MkdirAll(filepath.Join(dir, "pg_wal"), 0o700); err != nil {
		t.Fatal(err)
	}

	legacyEnd, pending, err := prepareTemplate1Migration(dir)
	if err != nil || !pending || legacyEnd == 0 {
		t.Fatalf("prepare = (%d, %v, %v), want a pending migration with a legacy end", legacyEnd, pending, err)
	}
	if !exists(template1LegacyWALEndFile) {
		t.Fatal("legacy WAL end not persisted before the copy")
	}
	if err := copyTemplate1ToBase1(dir); err != nil {
		t.Fatal(err)
	}
	if got := read("base/1/1259"); !bytes.Equal(got, []byte("template1-pg_class")) {
		t.Errorf("base/1/1259 = %q, want base/2's template1 pg_class", got)
	}
	if got := read("base/1/2615"); !bytes.Equal(got, []byte("pristine-pg_namespace")) {
		t.Errorf("base/1/2615 = %q, want template0's pristine image for the emptied file", got)
	}
	if exists("base/1/9999") {
		t.Error("stale base/1 file without a base/2 counterpart survived the copy")
	}

	// Restart inside the window: post-copy work in base/1 must survive.
	write("base/1/16400", []byte("template1-user-table-after-copy"))
	if _, pending, err := prepareTemplate1Migration(dir); err != nil || !pending {
		t.Fatalf("second prepare = (pending %v, %v), want still pending", pending, err)
	}
	if err := copyTemplate1ToBase1(dir); err != nil {
		t.Fatal(err)
	}
	if got := read("base/1/16400"); !bytes.Equal(got, []byte("template1-user-table-after-copy")) {
		t.Errorf("a restart re-copied base/2 over newer base/1 data: %q", got)
	}

	if err := finishTemplate1Migration(dir); err != nil {
		t.Fatal(err)
	}
	if !readGoopgFeatures(dir)[Template1AtBase1Feature] {
		t.Error("finish did not record the feature")
	}
	if exists("base/2") || exists(template1CopiedFile) {
		t.Error("finish left base/2 or the copy marker behind")
	}
	if _, pending, err := prepareTemplate1Migration(dir); err != nil || pending {
		t.Fatalf("prepare after finish = (pending %v, %v), want done", pending, err)
	}
	if exists(template1LegacyWALEndFile) {
		t.Error("legacy WAL end marker not retired after the migration finished")
	}
}
