package activity

import (
	"reflect"
	"testing"
)

// TestBaseBackupProgressRows pins pg_stat_progress_basebackup's row shape
// (system_views.sql: phase names, backup_total NULL while unknown) and the
// slot lifecycle (M0149-0077l).
func TestBaseBackupProgressRows(t *testing.T) {
	p := StartBaseBackupProgress(42)
	defer p.End()
	p.SetPhase(BaseBackupPhaseStreamBackup)
	p.SetTablespacesTotal(2)
	p.AddStreamed(8192)
	p.TablespaceDone()
	var mine []string
	for _, r := range BaseBackupProgressRows("NULL") {
		if r[0] == "42" {
			mine = r
		}
	}
	want := []string{"42", "streaming database files", "NULL", "8192", "2", "1"}
	if !reflect.DeepEqual(mine, want) {
		t.Fatalf("row = %v, want %v", mine, want)
	}
	p.End()
	for _, r := range BaseBackupProgressRows("NULL") {
		if r[0] == "42" {
			t.Fatal("row survives End")
		}
	}
	var nilSlot *BaseBackupProgress
	nilSlot.AddStreamed(1) // nil slots are no-ops (streamers built without progress)
	nilSlot.End()
}
