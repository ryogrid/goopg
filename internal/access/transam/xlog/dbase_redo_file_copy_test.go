package xlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

func fileCopyRec(db, spc, srcDB, srcSpc uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[0:], db)
	binary.LittleEndian.PutUint32(b[4:], spc)
	binary.LittleEndian.PutUint32(b[8:], srcDB)
	binary.LittleEndian.PutUint32(b[12:], srcSpc)
	return b
}

// TestDbaseRedoFileCopy pins dbase_redo's XLOG_DBASE_CREATE_FILE_COPY arm
// (M0149-0030c): the source database's files are copied into
// GetDatabasePath(db, tablespace) — a tablespace target lives under
// pg_tblspc/<spc>/<version dir>/<db> — and a missing tablespace directory is
// created with PG's WARNING once recovery is consistent (with
// allow_in_place_tablespaces), or is a fatal "missing directory" without it.
func TestDbaseRedoFileCopy(t *testing.T) {
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, "base", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "base", "1", "1259"), []byte("pg_class"), 0o600); err != nil {
		t.Fatal(err)
	}

	// pg_default target: base/16385 gets the template's files.
	SetDbaseRedoState(nil)
	if err := applyDbaseCreateFileCopyAt(data, fileCopyRec(16385, 1663, 1, 1663)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(data, "base", "16385", "1259")); err != nil || string(b) != "pg_class" {
		t.Fatalf("copied file = %q, %v", b, err)
	}

	// Tablespace target whose pg_tblspc/<spc> was removed, consistent, in-place allowed.
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)
	consistent, inPlace := true, true
	SetDbaseRedoState(&DbaseRedoState{Consistent: func() bool { return consistent }, AllowInPlaceTablespaces: func() bool { return inPlace }})
	defer SetDbaseRedoState(nil)
	if err := applyDbaseCreateFileCopyAt(data, fileCopyRec(16386, 16384, 1, 1663)); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("pg_tblspc", "16384", misc.TablespaceVersionDirectory)
	if !strings.Contains(logBuf.String(), "creating missing directory: "+want) {
		t.Fatalf("log lacks the WARNING for %s: %s", want, logBuf.String())
	}
	if _, err := os.Stat(filepath.Join(data, want, "16386", "1259")); err != nil {
		t.Fatalf("database not copied into the tablespace: %v", err)
	}

	// Without allow_in_place_tablespaces, a consistent standby PANICs.
	inPlace = false
	err := applyDbaseCreateFileCopyAt(data, fileCopyRec(16387, 16390, 1, 1663))
	var fatal *RecoveryFatalError
	if !errors.As(err, &fatal) || !strings.Contains(fatal.Msg, "missing directory") {
		t.Fatalf("err = %v, want the missing directory PANIC", err)
	}
}
