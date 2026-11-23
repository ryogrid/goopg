package storage

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

func TestParseFilenameForNontempRelation(t *testing.T) {
	type res struct {
		rel   uint64
		fork  ForkNumber
		segno uint32
		ok    bool
	}
	for name, want := range map[string]res{
		"16384":           {16384, MainFork, 0, true},
		"16384.2":         {16384, MainFork, 2, true},
		"16384_init":      {16384, InitFork, 0, true},
		"16384_init.1":    {16384, InitFork, 1, true},
		"16384_vm":        {16384, VisibilityMapFork, 0, true},
		"16384_fsm":       {16384, FSMFork, 0, true},
		"t3_16384":        {ok: false},
		"16384_foo":       {ok: false},
		"16384.":          {ok: false},
		"PG_VERSION":      {ok: false},
		"pg_filenode.map": {ok: false},
	} {
		rel, fork, segno, ok := parseFilenameForNontempRelation(name)
		if got := (res{rel, fork, segno, ok}); ok != want.ok || (ok && got != want) {
			t.Errorf("parse(%q) = %+v, want %+v", name, got, want)
		}
	}
}

// TestResetUnloggedRelations pins reinit.c's two passes: cleanup removes
// every fork but _init of a relation that has an init fork (logged
// relations are untouched), and init copies _init over the main fork, in
// base/ and in a tablespace's version directory.
func TestResetUnloggedRelations(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "base", "5")
	tsLoc := t.TempDir()
	tsDB := filepath.Join(tsLoc, misc.TablespaceVersionDirectory, "5")
	for _, d := range []string{base, tsDB, filepath.Join(dataDir, "pg_tblspc")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(tsLoc, filepath.Join(dataDir, "pg_tblspc", "16400")); err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{base, tsDB} {
		write(dir, "100", "main-data")
		write(dir, "100_init", "init")
		write(dir, "100_vm", "TEST_VM")
		write(dir, "100_fsm", "TEST_FSM")
		write(dir, "200", "logged")
		write(dir, "200_vm", "logged-vm")
	}
	os.Remove(filepath.Join(tsDB, "100")) // the test unlinks a main fork

	if err := ResetUnloggedRelations(dataDir, UnloggedRelationCleanup); err != nil {
		t.Fatal(err)
	}
	list := func(dir string) []string {
		ents, _ := os.ReadDir(dir)
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		return names
	}
	for _, dir := range []string{base, tsDB} {
		if got := list(dir); len(got) != 3 || got[0] != "100_init" || got[1] != "200" || got[2] != "200_vm" {
			t.Fatalf("%s after cleanup = %v, want [100_init 200 200_vm]", dir, got)
		}
	}
	if err := ResetUnloggedRelations(dataDir, UnloggedRelationInit); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, tsDB} {
		b, err := os.ReadFile(filepath.Join(dir, "100"))
		if err != nil || string(b) != "init" {
			t.Fatalf("%s/100 after init = %q (%v), want the init fork's contents", dir, b, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "200")); string(b) != "logged" {
			t.Fatalf("logged relation changed: %q", b)
		}
	}
}
