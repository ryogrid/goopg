package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TablespaceDirError is a create_tablespace_directories failure with PG's
// SQLSTATE, for the executor to raise verbatim.
type TablespaceDirError struct {
	Code    string
	Message string
	Hint    string
}

func (e *TablespaceDirError) Error() string { return e.Message }

// CreateTablespaceDirectories ports create_tablespace_directories
// (commands/tablespace.c). An in-place tablespace (empty location) is a real
// directory pg_tblspc/<oid>. An external one is a symlink pg_tblspc/<oid> ->
// location, as PG makes it. In both cases the relation files live under the
// version subdirectory PG_<major>_<catversion>, which is created here. Relation
// paths (pg_tblspc/<oid>/<version>/<db>/<relfilenode>) therefore resolve
// through the symlink unchanged.
//
// In recovery (tblspc_redo) a version directory left by an earlier attempt is
// reused and an existing link is replaced, as PG does. M0149-0003.
func CreateTablespaceDirectories(dataDir string, oid uint32, location string, inRecovery bool) error {
	linkloc := filepath.Join(dataDir, "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
	inPlace := location == ""
	if inPlace {
		// In-place: pg_tblspc/<oid> is the tablespace directory itself.
		if err := os.MkdirAll(linkloc, 0o700); err != nil {
			return fmt.Errorf("could not create directory %q: %w", linkloc, err)
		}
		location = linkloc
	}
	versionDir := filepath.Join(location, misc.TablespaceVersionDirectory)

	// Attempt to coerce target directory to safe permissions. If this
	// fails, it doesn't exist or has the wrong owner.
	if err := os.Chmod(location, 0o700); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			e := &TablespaceDirError{Code: "58P01", Message: fmt.Sprintf("directory %q does not exist", location)}
			if inRecovery {
				e.Hint = "Create this directory for the tablespace before restarting the server."
			}
			return e
		}
		return &TablespaceDirError{Code: "58030", Message: fmt.Sprintf("could not set permissions on directory %q: %v", location, err)}
	}

	// The creation of the version directory prevents more than one
	// tablespace in a single location.
	if st, err := os.Lstat(versionDir); err == nil {
		if !inRecovery {
			return &TablespaceDirError{Code: "55006", Message: fmt.Sprintf("directory %q already in use as a tablespace", versionDir)}
		}
		if !st.IsDir() {
			return &TablespaceDirError{Code: "58030", Message: fmt.Sprintf("%q exists but is not a directory", versionDir)}
		}
	} else if err := os.Mkdir(versionDir, 0o700); err != nil {
		return &TablespaceDirError{Code: "58030", Message: fmt.Sprintf("could not create directory %q: %v", versionDir, err)}
	}

	if inPlace {
		return nil
	}
	// In recovery, remove an old symlink, in case it points to the wrong place.
	if inRecovery {
		if st, err := os.Lstat(linkloc); err == nil {
			if st.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(linkloc)
			} else if st.IsDir() {
				_ = os.RemoveAll(linkloc)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(linkloc), 0o700); err != nil {
		return fmt.Errorf("could not create directory %q: %w", filepath.Dir(linkloc), err)
	}
	if err := os.Symlink(location, linkloc); err != nil {
		return &TablespaceDirError{Code: "58030", Message: fmt.Sprintf("could not create symbolic link %q: %v", linkloc, err)}
	}
	return nil
}

// DestroyTablespaceDirectories is destroy_tablespace_directories
// (commands/tablespace.c) for an empty tablespace: it removes the version
// directory and pg_tblspc/<oid>. For an external tablespace the latter is the
// symlink, and the location directory itself stays, as in PG.
func DestroyTablespaceDirectories(dataDir string, oid uint32) error {
	linkloc := filepath.Join(dataDir, "pg_tblspc", strconv.FormatUint(uint64(oid), 10))
	st, err := os.Lstat(linkloc)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(linkloc)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(target, misc.TablespaceVersionDirectory)); err != nil {
			return err
		}
		return os.Remove(linkloc)
	}
	return os.RemoveAll(linkloc)
}
