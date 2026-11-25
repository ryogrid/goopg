package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/utils/misc"
)

// ResetUnloggedRelations operations (storage/reinit.h).
const (
	// UnloggedRelationCleanup removes every fork but the init fork of each
	// relation that has one. StartupXLOG runs it before redo.
	UnloggedRelationCleanup = 0x0001
	// UnloggedRelationInit copies each init fork over its main fork.
	// StartupXLOG runs it after redo.
	UnloggedRelationInit = 0x0002
)

// ResetUnloggedRelations ports storage/file/reinit.c. After a crash an
// unlogged relation's contents are undefined, so it is reset to its init
// fork. The cleanup and init passes cover base/<db> and every
// pg_tblspc/<oid>/<version>/<db>. M0149-0003.
func ResetUnloggedRelations(dataDir string, op int) error {
	if err := resetUnloggedRelationsInTablespaceDir(filepath.Join(dataDir, "base"), op); err != nil {
		return err
	}
	tsRoot := filepath.Join(dataDir, "pg_tblspc")
	ents, err := os.ReadDir(tsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if _, perr := strconv.ParseUint(e.Name(), 10, 32); perr != nil {
			continue
		}
		dir := filepath.Join(tsRoot, e.Name(), misc.TablespaceVersionDirectory)
		if err := resetUnloggedRelationsInTablespaceDir(dir, op); err != nil {
			return err
		}
	}
	return nil
}

// resetUnloggedRelationsInTablespaceDir visits each database directory (an
// all-digit name) of a tablespace directory. A missing directory is not an
// error: a tablespace created as a symlink may point nowhere yet.
func resetUnloggedRelationsInTablespaceDir(tsDir string, op int) error {
	ents, err := os.ReadDir(tsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		if _, perr := strconv.ParseUint(e.Name(), 10, 32); perr != nil {
			continue
		}
		if err := resetUnloggedRelationsInDbspaceDir(filepath.Join(tsDir, e.Name()), op); err != nil {
			return err
		}
	}
	return nil
}

func resetUnloggedRelationsInDbspaceDir(dir string, op int) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if op&UnloggedRelationCleanup != 0 {
		unlogged := map[uint64]bool{}
		for _, e := range ents {
			if rel, fork, _, ok := parseFilenameForNontempRelation(e.Name()); ok && fork == InitFork {
				unlogged[rel] = true
			}
		}
		if len(unlogged) > 0 {
			for _, e := range ents {
				rel, fork, _, ok := parseFilenameForNontempRelation(e.Name())
				if !ok || fork == InitFork || !unlogged[rel] {
					continue
				}
				p := filepath.Join(dir, e.Name())
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("could not remove file %q: %w", p, err)
				}
			}
		}
	}
	if op&UnloggedRelationInit != 0 {
		var copied []string
		for _, e := range ents {
			rel, fork, segno, ok := parseFilenameForNontempRelation(e.Name())
			if !ok || fork != InitFork {
				continue
			}
			dst := strconv.FormatUint(rel, 10)
			if segno != 0 {
				dst += "." + strconv.FormatUint(uint64(segno), 10)
			}
			if err := copyRelationFile(filepath.Join(dir, e.Name()), filepath.Join(dir, dst)); err != nil {
				return err
			}
			copied = append(copied, dst)
		}
		// copy_file does not fsync; PG fsyncs every new main fork and then
		// the directory, so the reset survives a crash right after startup.
		for _, name := range copied {
			if err := fsyncPath(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
		if len(copied) > 0 {
			if err := fsyncPath(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// parseFilenameForNontempRelation is reinit.c's parse_filename_for_nontemp_relation:
// "<relnumber>[_<fork>][.<segno>]" with fork one of fsm, vm, init.
func parseFilenameForNontempRelation(name string) (rel uint64, fork ForkNumber, segno uint32, ok bool) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 || i > 10 {
		return 0, 0, 0, false
	}
	rel, err := strconv.ParseUint(name[:i], 10, 32)
	if err != nil || rel == 0 {
		return 0, 0, 0, false
	}
	rest := name[i:]
	fork = MainFork
	if strings.HasPrefix(rest, "_") {
		forkName := rest[1:]
		if dot := strings.IndexByte(forkName, '.'); dot >= 0 {
			forkName, rest = forkName[:dot], forkName[dot:]
		} else {
			rest = ""
		}
		switch forkName {
		case "fsm":
			fork = FSMFork
		case "vm":
			fork = VisibilityMapFork
		case "init":
			fork = InitFork
		default:
			return 0, 0, 0, false
		}
	}
	if rest != "" {
		if rest[0] != '.' || len(rest) == 1 {
			return 0, 0, 0, false
		}
		n, err := strconv.ParseUint(rest[1:], 10, 32)
		if err != nil || n == 0 {
			return 0, 0, 0, false
		}
		segno = uint32(n)
	}
	return rel, fork, segno, true
}

// copyRelationFile is copy_file (storage/file/copydir.c): it replaces dst
// with the contents of src.
func copyRelationFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("could not open file %q: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("could not create file %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("could not write file %q: %w", dst, err)
	}
	return out.Close()
}

func fsyncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
