package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLooksLikeTempRelName pins fd.c's looks_like_temp_rel_name.
func TestLooksLikeTempRelName(t *testing.T) {
	for name, want := range map[string]bool{
		"t3_16384":        true,
		"t3_16384_fsm":    true,
		"t3_16384_vm":     true,
		"t3_16384_init":   true,
		"t3_16384.1":      true,
		"t3_16384_fsm.2":  true,
		"16384":           false,
		"t_16384":         false,
		"t3_":             false,
		"t3_16384_foo":    false,
		"t3_16384.":       false,
		"t3_16384.1x":     false,
		"pg_filenode.map": false,
	} {
		if got := looksLikeTempRelName(name); got != want {
			t.Errorf("looksLikeTempRelName(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestTempRelPathAndStartupSweep checks that a temporary relation's forks
// are named t<backend>_<relfilenode> and that RemovePgTempRelationFiles
// deletes them while leaving permanent relations alone. M0149-0006a.
func TestTempRelPathAndStartupSweep(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(ManagerConfig{DataDir: dir})
	tmp := RelFileNode{DBOid: 5, RelOid: 16860, Fork: MainFork}
	perm := RelFileNode{DBOid: 5, RelOid: 16861, Fork: MainFork}
	m.SetRelTemp(tmp, 7, true)
	if got := m.RelPath(tmp); got != "base/5/t7_16860" {
		t.Fatalf("RelPath(temp) = %q", got)
	}
	fsm := tmp
	fsm.Fork = FSMFork
	if got := m.RelPath(fsm); got != "base/5/t7_16860_fsm" {
		t.Fatalf("RelPath(temp fsm) = %q", got)
	}
	for _, r := range []RelFileNode{tmp, fsm, perm} {
		if err := m.CreateFile(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	RemovePgTempRelationFiles(dir)
	for name, want := range map[string]bool{"t7_16860": false, "t7_16860_fsm": false, "16861": true} {
		_, err := os.Stat(filepath.Join(dir, "base", "5", name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v after sweep, want %v", name, err == nil, want)
		}
	}
}
