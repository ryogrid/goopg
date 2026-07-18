package cluster

// initdb template caching for throwaway test clusters, modeled on upstream
// PostgreSQL 16's INITDB_TEMPLATE (Cluster.pm:640-667: node creation copies a
// pre-initialized data dir instead of re-running initdb). A full `goopg init`
// produces a position-independent ~9 MB tree, so per test process we init each
// distinct argument set exactly once and copy it per cluster, then
// re-randomize the cluster system identifier in the copy.
//
// Re-identification is mandatory, not optional: `goopg init` mints a random
// sysid (global/system_identifier, embedded in pg_control and in every
// PG-compatible WAL page header, where pg_waldump cross-checks it). Naive
// copies would give every test cluster in a process the SAME sysid, blinding
// the sysid-mismatch rejection path in every test that pairs two
// independently-created clusters (failover/replication E2Es). See
// ci/design/test-gate-speedups/04-parallelism-and-bootstrap-caching.md §1.
//
// Refusals (fall back to a direct init, never cache):
//   - arg sets containing -X/--waldir: init then creates pg_wal as a symlink
//     to ONE absolute WAL dir; a copied template would make every clone write
//     the SAME physical WAL directory.
//   - any symlink encountered during the copy (defense in depth): the copy
//     WalkDirs without following links and refuses rather than replicates.
//
// Staleness is impossible by construction: the map is per test process
// (a rebuilt goopg binary can never see an older binary's template), and
// SyncInit clusters bypass the template entirely (Cluster.initDataDir).

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	templateMu   sync.Mutex
	templateDirs = map[string]string{} // canonicalized init identity -> template dir
)

// pg_control layout facts (mirroring internal/initdb/pgcontrol.go, which is
// not importable from every test package that links this harness — the
// values are pinned by TestTemplateCloneEquivalence):
const (
	pgControlCRCOffset = 292 // offsetof(ControlFileData, crc) on x86_64
	// bootstrapWALSegment is the single segment a fresh init writes; its
	// XLogLongPageHeader carries the sysid at bytes [24:32] (no page CRC —
	// only per-record CRCs exist, and they do not cover the page header).
	bootstrapWALSegment = "000000010000000000000001"
	walSysIDOffset      = 24
)

var crcCastagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// templateRefused marks argument sets the cache must not serve.
func templateRefused(initArgs []string) bool {
	for _, a := range initArgs {
		if a == "-X" || a == "--waldir" || strings.HasPrefix(a, "--waldir=") || strings.HasPrefix(a, "-X=") {
			return true
		}
	}
	return false
}

// initFromTemplate materializes c.dataDir as a re-identified copy of the
// per-process template for c's init identity. Returns (false, nil) when the
// argument set is refused (caller runs a direct init); any error is a
// template/copy failure the caller should also resolve with a direct init.
func (c *Cluster) initFromTemplate() (bool, error) {
	if templateRefused(c.initArgs) {
		return false, nil
	}
	tpl, err := c.templateFor()
	if err != nil {
		return false, err
	}
	if err := os.RemoveAll(c.dataDir); err != nil {
		return false, fmt.Errorf("template clone: clear %s: %w", c.dataDir, err)
	}
	if err := copyTreeNoSymlinks(tpl, c.dataDir); err != nil {
		// A half-copied data dir must not survive for the direct-init
		// fallback to trip over.
		_ = os.RemoveAll(c.dataDir)
		return false, err
	}
	if err := reRandomizeSysID(c.dataDir); err != nil {
		_ = os.RemoveAll(c.dataDir)
		return false, err
	}
	return true, nil
}

// templateFor returns (creating on first use) the template dir for c's init
// identity: repo root + goopg command + full init argument vector. Templates
// are always inited with --no-sync (a template is throwaway by definition)
// and live under os.TempDir() until the OS tempdir hygiene collects them
// (~9 MB each, a handful per process).
func (c *Cluster) templateFor() (string, error) {
	args := append([]string{"--no-sync"}, c.initArgs...)
	key := c.repoRoot + "\x00" + strings.Join(c.goopgCommand, "\x00") + "\x00\x00" + strings.Join(args, "\x00")
	templateMu.Lock()
	defer templateMu.Unlock()
	if dir, ok := templateDirs[key]; ok {
		return dir, nil
	}
	dir, err := os.MkdirTemp("", "goopg-initdb-template-")
	if err != nil {
		return "", err
	}
	tplData := filepath.Join(dir, "data")
	res, err := c.runGoopg(append([]string{"init", "-D", tplData}, args...)...)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if res.ExitCode != 0 {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("template init failed: %s", strings.TrimSpace(res.Stderr))
	}
	templateDirs[key] = tplData
	return tplData, nil
}

// copyTreeNoSymlinks copies src into dst (created fresh), preserving file
// modes. It never follows links: encountering ANY symlink aborts the copy —
// a symlinked pg_wal would alias one physical WAL dir across clones.
func copyTreeNoSymlinks(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("template copy: refusing symlink %s", path)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case !info.Mode().IsRegular():
			return fmt.Errorf("template copy: refusing non-regular file %s", path)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// reRandomizeSysID re-identifies a cloned data dir: a fresh random sysid is
// stamped into all three surfaces `goopg init` writes it to —
// global/system_identifier (8-byte LE), pg_control bytes [0:8] (+ CRC32C over
// [0:292] into [292:296]), and the bootstrap WAL segment's long page header
// at bytes [24:32] (no CRC covers it). TestTemplateCloneEquivalence pins that
// the result is self-consistent and differs from the template's sysid.
func reRandomizeSysID(dataDir string) error {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Errorf("re-identify: generate sysid: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "global", "system_identifier"), buf[:], 0o600); err != nil {
		return fmt.Errorf("re-identify: system_identifier: %w", err)
	}

	ctrlPath := filepath.Join(dataDir, "global", "pg_control")
	ctrl, err := os.ReadFile(ctrlPath)
	if err != nil {
		return fmt.Errorf("re-identify: read pg_control: %w", err)
	}
	if len(ctrl) < pgControlCRCOffset+4 {
		return fmt.Errorf("re-identify: pg_control too short (%d bytes)", len(ctrl))
	}
	copy(ctrl[0:8], buf[:])
	binary.LittleEndian.PutUint32(ctrl[pgControlCRCOffset:], crc32.Checksum(ctrl[:pgControlCRCOffset], crcCastagnoliTable))
	if err := os.WriteFile(ctrlPath, ctrl, 0o600); err != nil {
		return fmt.Errorf("re-identify: write pg_control: %w", err)
	}

	segPath := filepath.Join(dataDir, "pg_wal", bootstrapWALSegment)
	seg, err := os.OpenFile(segPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("re-identify: open bootstrap WAL segment: %w", err)
	}
	if _, err := seg.WriteAt(buf[:], walSysIDOffset); err != nil {
		seg.Close()
		return fmt.Errorf("re-identify: stamp WAL long header: %w", err)
	}
	return seg.Close()
}
