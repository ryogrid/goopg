package xlog

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/utils/misc"
)

// DbaseRedoState is what dbase_redo consults while replaying
// XLOG_DBASE_CREATE_FILE_COPY: whether recovery has reached consistency
// (reachedConsistency) and allow_in_place_tablespaces. A standby installs it
// (SetDbaseRedoState); crash recovery without it is not yet consistent.
// M0149-0030c.
type DbaseRedoState struct {
	Consistent              func() bool
	AllowInPlaceTablespaces func() bool
}

var dbaseRedoState atomic.Pointer[DbaseRedoState]

// SetDbaseRedoState installs the consistency and allow_in_place_tablespaces
// sources dbase_redo's recovery_create_dbdir needs.
func SetDbaseRedoState(s *DbaseRedoState) { dbaseRedoState.Store(s) }

const (
	defaultTablespaceOID = 1663 // DEFAULTTABLESPACE_OID
	globalTablespaceOID  = 1664 // GLOBALTABLESPACE_OID
)

// databasePath is GetDatabasePath (common/relpath.c), relative to the data
// directory: global for pg_global, base/<db> for pg_default, else
// pg_tblspc/<spc>/<version dir>/<db>.
func databasePath(dbOID, spcOID uint32) string {
	switch spcOID {
	case globalTablespaceOID:
		return "global"
	case defaultTablespaceOID, 0:
		return "base/" + strconv.FormatUint(uint64(dbOID), 10)
	}
	return "pg_tblspc/" + strconv.FormatUint(uint64(spcOID), 10) + "/" +
		misc.TablespaceVersionDirectory + "/" + strconv.FormatUint(uint64(dbOID), 10)
}

// decodeXLogDbaseCreateFileCopy parses xl_dbase_create_file_copy_rec:
// {db_id, tablespace_id, src_db_id, src_tablespace_id} (dbcommands_xlog.h).
func decodeXLogDbaseCreateFileCopy(mainData []byte) (db, spc, srcDB, srcSpc uint32, err error) {
	if len(mainData) < 16 {
		return 0, 0, 0, 0, fmt.Errorf("wal: xl_dbase_create_file_copy_rec main-data len %d (want >= 16)", len(mainData))
	}
	le := binary.LittleEndian
	return le.Uint32(mainData[0:4]), le.Uint32(mainData[4:8]), le.Uint32(mainData[8:12]), le.Uint32(mainData[12:16]), nil
}

// recoveryCreateDBDir is recovery_create_dbdir (dbcommands.c): a directory
// CREATE DATABASE replay needs but that is missing is created, with a WARNING
// once recovery is consistent (DEBUG1 before), and is a PANIC when consistent
// without allow_in_place_tablespaces; onlyTblspc restricts it to a path under
// pg_tblspc/.
func recoveryCreateDBDir(dataDir, rel string, onlyTblspc bool) error {
	path := filepath.Join(dataDir, rel)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if onlyTblspc && !strings.Contains(rel, "pg_tblspc/") {
		return &RecoveryFatalError{Msg: fmt.Sprintf("requested to created invalid directory: %s", rel)}
	}
	consistent, inPlace := false, false
	if st := dbaseRedoState.Load(); st != nil {
		if st.Consistent != nil {
			consistent = st.Consistent()
		}
		if st.AllowInPlaceTablespaces != nil {
			inPlace = st.AllowInPlaceTablespaces()
		}
	}
	if consistent && !inPlace {
		return &RecoveryFatalError{Msg: fmt.Sprintf("missing directory \"%s\"", rel)}
	}
	if consistent {
		slog.Warn("creating missing directory: " + rel)
	} else {
		slog.Debug("creating missing directory: " + rel)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return &RecoveryFatalError{Msg: fmt.Sprintf("could not create missing directory \"%s\": %v", rel, err)}
	}
	return nil
}

// applyDbaseCreateFileCopy is dbase_redo's XLOG_DBASE_CREATE_FILE_COPY arm:
// drop the target directory if present, create a missing parent (under
// pg_tblspc) and a missing source as recovery_create_dbdir does, then copy
// the source database directory's files into the target (copydir, no
// recursion). Replay writes through the storage manager, so the source
// files on disk are current (PG's FlushDatabaseBuffers). M0149-0030c.
func applyDbaseCreateFileCopy(mgr *storage.Manager, mainData []byte) error {
	return applyDbaseCreateFileCopyAt(mgr.DataDir(), mainData)
}

func applyDbaseCreateFileCopyAt(dataDir string, mainData []byte) error {
	db, spc, srcDB, srcSpc, err := decodeXLogDbaseCreateFileCopy(mainData)
	if err != nil {
		return err
	}
	srcRel, dstRel := databasePath(srcDB, srcSpc), databasePath(db, spc)
	dst := filepath.Join(dataDir, dstRel)
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			slog.Warn(fmt.Sprintf("some useless files may be left behind in old database directory \"%s\"", dstRel))
		}
	}
	if parent := filepath.Dir(dstRel); parent != "." {
		if _, err := os.Stat(filepath.Join(dataDir, parent)); os.IsNotExist(err) {
			if err := recoveryCreateDBDir(dataDir, parent, true); err != nil {
				return err
			}
		} else if err != nil {
			return &RecoveryFatalError{Msg: fmt.Sprintf("could not stat directory \"%s\": %v", dstRel, err)}
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, srcRel)); os.IsNotExist(err) {
		if err := recoveryCreateDBDir(dataDir, srcRel, false); err != nil {
			return err
		}
	}
	return copyDatabaseDir(filepath.Join(dataDir, srcRel), dst)
}

// copyDatabaseDir is copydir(src, dst, false): create dst and copy every
// regular file of src into it.
func copyDatabaseDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("wal: dbase-create replay: could not create directory %q: %w", dst, err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("wal: dbase-create replay: could not open directory %q: %w", src, err)
	}
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyRegularFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("wal: dbase-create replay: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("wal: dbase-create replay: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("wal: dbase-create replay: copy %q: %w", from, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("wal: dbase-create replay: fsync %q: %w", to, err)
	}
	return out.Close()
}
