package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/utils/misc"
)

// SetRelTemp marks rel (any fork; the relation is meant) as a temporary
// relation of the backend numbered backend, or clears the mark. A temporary
// relation's files are named t<backend>_<relfilenode>[_<fork>]
// (relpathbackend, common/relpath.c), so RemovePgTempRelationFiles can find
// and delete them at the next startup. M0149-0006a.
//
// The mark must be set before the relation's first file is opened: the
// manager caches open files by RelFileNode, and a later open after eviction
// would otherwise use the other name.
func (m *Manager) SetRelTemp(rel RelFileNode, backend uint32, on bool) {
	if m == nil {
		return
	}
	key := rel.Canonical()
	key.Fork = MainFork
	if on {
		m.tempRels.Store(key, backend)
	} else {
		m.tempRels.Delete(key)
	}
}

// relFileStem is the file-name stem of rel's forks: "<relfilenode>", or
// "t<backend>_<relfilenode>" for a temporary relation.
func (m *Manager) relFileStem(rel RelFileNode) string {
	key := rel
	key.Fork = MainFork
	if v, ok := m.tempRels.Load(key); ok {
		return "t" + strconv.FormatUint(uint64(v.(uint32)), 10) + "_" + strconv.FormatUint(uint64(rel.RelOid), 10)
	}
	return strconv.FormatUint(uint64(rel.RelOid), 10)
}

// RemovePgTempRelationFiles ports the relation-file half of RemovePgTempFiles
// (storage/file/fd.c): every startup removes the files of temporary
// relations, which no backend can still use, in base/<db> and every
// pg_tblspc/<oid>/<version>/<db>. Errors are ignored, as PG only logs them.
func RemovePgTempRelationFiles(dataDir string) {
	removeTempRelFilesInTablespace(filepath.Join(dataDir, "base"))
	tsRoot := filepath.Join(dataDir, "pg_tblspc")
	ents, err := os.ReadDir(tsRoot)
	if err != nil {
		return
	}
	for _, e := range ents {
		if _, perr := strconv.ParseUint(e.Name(), 10, 32); perr != nil {
			continue
		}
		removeTempRelFilesInTablespace(filepath.Join(tsRoot, e.Name(), misc.TablespaceVersionDirectory))
	}
}

// removeTempRelFilesInTablespace is RemovePgTempRelationFiles: it visits the
// all-digit database directories of a tablespace directory.
func removeTempRelFilesInTablespace(tsDir string) {
	ents, err := os.ReadDir(tsDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() || strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		dbDir := filepath.Join(tsDir, e.Name())
		files, err := os.ReadDir(dbDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if looksLikeTempRelName(f.Name()) {
				_ = os.Remove(filepath.Join(dbDir, f.Name()))
			}
		}
	}
}

// looksLikeTempRelName is fd.c's looks_like_temp_rel_name:
// t<digits>_<digits>, then an optional _<forkname>, then an optional
// .<segment>.
func looksLikeTempRelName(name string) bool {
	if !strings.HasPrefix(name, "t") {
		return false
	}
	rest := name[1:]
	n := leadingDigits(rest)
	if n == 0 || n >= len(rest) || rest[n] != '_' {
		return false
	}
	rest = rest[n+1:]
	n = leadingDigits(rest)
	if n == 0 {
		return false
	}
	rest = rest[n:]
	if strings.HasPrefix(rest, "_") {
		matched := false
		for _, fork := range []string{"_fsm", "_vm", "_init"} {
			if strings.HasPrefix(rest, fork) {
				rest = rest[len(fork):]
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if rest == "" {
		return true
	}
	if rest[0] != '.' {
		return false
	}
	n = leadingDigits(rest[1:])
	return n > 0 && n == len(rest)-1
}

func leadingDigits(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
