package executor

import (
	"strings"
	"testing"
	"time"
)

// TestRecordOutFieldQuoting pins record_out's quoting for pg_backup_stop's
// labelfile column: plain words stay bare, and whitespace, quotes,
// backslashes, commas, parentheses and the empty string force quotes with
// doubled quote and backslash characters. M0149-0077k.
func TestRecordOutFieldQuoting(t *testing.T) {
	cases := map[string]string{
		"abc":       "abc",
		"":          `""`,
		"a b":       `"a b"`,
		"x\ny":      "\"x\ny\"",
		`q"q`:       `"q""q"`,
		`b\s`:       `"b\\s"`,
		"(1,2)":     `"(1,2)"`,
		"0/1000388": "0/1000388",
	}
	for in, want := range cases {
		if got := recordOutField(in); got != want {
			t.Errorf("recordOutField(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSessionBackupState pins the per-session backup lifecycle the
// BASE_BACKUP refusal and the disconnect abort rely on.
func TestSessionBackupState(t *testing.T) {
	const pn = int32(31999)
	if SessionBackupRunning(pn) {
		t.Fatal("fresh slot reports a running backup")
	}
	sessionBackups.Store(pn, &sessionBackup{label: "x"})
	if !SessionBackupRunning(pn) {
		t.Fatal("stored backup not running")
	}
	if !AbortSessionBackup(pn) || SessionBackupRunning(pn) {
		t.Fatal("abort did not end the backup")
	}
	if AbortSessionBackup(pn) {
		t.Fatal("second abort reported a backup")
	}
}

// TestBuildBackupLabel pins backup_label's streamed-backup layout
// (build_backup_content).
func TestBuildBackupLabel(t *testing.T) {
	ts := time.Date(2026, 11, 24, 13, 37, 13, 0, time.UTC)
	got := string(BuildBackupLabel("x", 0x39000028, 0x39000080, 1, 16<<20, ts))
	want := "START WAL LOCATION: 0/39000028 (file 000000010000000000000039)\n" +
		"CHECKPOINT LOCATION: 0/39000080\n" +
		"BACKUP METHOD: streamed\n" +
		"BACKUP FROM: primary\n" +
		"START TIME: 2026-11-24 13:37:13 UTC\n" +
		"LABEL: x\n" +
		"START TIMELINE: 1\n"
	if got != want {
		t.Fatalf("label:\n%s\nwant:\n%s", got, want)
	}
	if !strings.HasPrefix(recordOutField(got), `"START WAL`) {
		t.Fatal("label field not quoted")
	}
}
