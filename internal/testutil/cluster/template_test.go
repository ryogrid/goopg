package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// TestTemplateCloneEquivalence is the correctness guard of
// ci/design/test-gate-speedups/04 §1.4: a data dir cloned from the init
// template must be structurally identical to a directly-inited one, except
// for exactly the re-identification surface — which is also exactly where a
// copy bug would hide, so those files are checked structurally rather than
// skipped:
//
//   - global/system_identifier — the 8-byte sysid is freshly randomized per
//     clone (and MUST differ from the template's, or paired-cluster tests
//     lose the sysid-mismatch rejection path).
//   - global/pg_control — differs in sysid ([0:8]), CRC ([292:296]), and the
//     init-time timestamp/nonce; every field visible outside those must
//     match, verified by comparing the byte ranges the clone path actually
//     rewrites against the template it was copied from.
//   - pg_wal/000000010000000000000001 — differs ONLY in the long page
//     header's sysid bytes [24:32] vs the template (bootstrap record bytes,
//     including their CRCs, are untouched); vs a direct init it also differs
//     in record timestamps, so the WAL comparison is structural (decoded
//     long header) plus byte-vs-template.
//
// Everything else must be byte-identical to a direct init with the same
// argument vector.
func TestTemplateCloneEquivalence(t *testing.T) {
	root := repoRoot(t)

	direct, err := New("tpl-equiv-direct", Options{
		RepoRoot: root,
		DataDir:  filepath.Join(t.TempDir(), "data"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Direct init, same --no-sync polarity the template uses.
	if err := direct.initDataDirDirect(); err != nil {
		t.Fatalf("direct init: %v", err)
	}

	clone, err := New("tpl-equiv-clone", Options{
		RepoRoot: root,
		DataDir:  filepath.Join(t.TempDir(), "data"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := clone.initFromTemplate()
	if err != nil || !ok {
		t.Fatalf("template init: ok=%v err=%v", ok, err)
	}
	tpl, err := clone.templateFor()
	if err != nil {
		t.Fatal(err)
	}

	excluded := map[string]bool{
		"global/system_identifier":      true, // re-randomized per clone (checked below)
		"global/pg_control":             true, // sysid+CRC rewritten (checked below)
		"pg_wal/" + bootstrapWALSegment: true, // long-header sysid rewritten (checked below)
	}

	directFiles := hashTree(t, direct.dataDir)
	cloneFiles := hashTree(t, clone.dataDir)
	for rel, sum := range directFiles {
		csum, ok := cloneFiles[rel]
		if !ok {
			t.Errorf("clone missing %s", rel)
			continue
		}
		if !excluded[rel] && sum != csum {
			t.Errorf("clone differs from direct init at %s", rel)
		}
	}
	for rel := range cloneFiles {
		if _, ok := directFiles[rel]; !ok {
			t.Errorf("clone has extra file %s", rel)
		}
	}

	// --- sysid consistency and freshness -------------------------------
	tplSys := readSysID(t, tpl)
	cloneSys := readSysID(t, clone.dataDir)
	directSys := readSysID(t, direct.dataDir)
	if cloneSys == tplSys {
		t.Fatalf("clone sysid %016x MUST differ from template sysid", cloneSys)
	}
	if cloneSys == directSys {
		t.Fatalf("clone sysid %016x collides with the direct init's", cloneSys)
	}

	// pg_control: the clone must equal its template byte-for-byte outside
	// the two ranges reRandomizeSysID rewrites, its sysid bytes must carry
	// cloneSys, and the CRC must verify.
	tplCtrl := readFileBytes(t, filepath.Join(tpl, "global", "pg_control"))
	cloneCtrl := readFileBytes(t, filepath.Join(clone.dataDir, "global", "pg_control"))
	if len(tplCtrl) != len(cloneCtrl) {
		t.Fatalf("pg_control size drift: template %d, clone %d", len(tplCtrl), len(cloneCtrl))
	}
	if got := binary.LittleEndian.Uint64(cloneCtrl[0:8]); got != cloneSys {
		t.Fatalf("pg_control sysid = %016x, want %016x", got, cloneSys)
	}
	wantCRC := crc32.Checksum(cloneCtrl[:pgControlCRCOffset], crcCastagnoliTable)
	if got := binary.LittleEndian.Uint32(cloneCtrl[pgControlCRCOffset:]); got != wantCRC {
		t.Fatalf("pg_control CRC = %08x, want %08x", got, wantCRC)
	}
	if !bytes.Equal(tplCtrl[8:pgControlCRCOffset], cloneCtrl[8:pgControlCRCOffset]) {
		t.Fatal("pg_control differs from template outside the sysid bytes")
	}
	if !bytes.Equal(tplCtrl[pgControlCRCOffset+4:], cloneCtrl[pgControlCRCOffset+4:]) {
		t.Fatal("pg_control differs from template after the CRC field")
	}

	// Bootstrap WAL segment: byte-identical to the template outside the
	// long header's sysid field, and the decoded long headers of clone vs
	// DIRECT init must agree on every field except SysID.
	tplSeg := readFileBytes(t, filepath.Join(tpl, "pg_wal", bootstrapWALSegment))
	cloneSeg := readFileBytes(t, filepath.Join(clone.dataDir, "pg_wal", bootstrapWALSegment))
	if len(tplSeg) != len(cloneSeg) {
		t.Fatalf("WAL segment size drift: template %d, clone %d", len(tplSeg), len(cloneSeg))
	}
	if !bytes.Equal(tplSeg[:walSysIDOffset], cloneSeg[:walSysIDOffset]) ||
		!bytes.Equal(tplSeg[walSysIDOffset+8:], cloneSeg[walSysIDOffset+8:]) {
		t.Fatal("WAL segment differs from template outside the long-header sysid")
	}
	directSeg := readFileBytes(t, filepath.Join(direct.dataDir, "pg_wal", bootstrapWALSegment))
	dh, err := xlog.DecodeXLogLongPageHeader(directSeg)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := xlog.DecodeXLogLongPageHeader(cloneSeg)
	if err != nil {
		t.Fatal(err)
	}
	if ch.SysID != cloneSys {
		t.Fatalf("WAL long-header sysid = %016x, want %016x", ch.SysID, cloneSys)
	}
	dh.SysID, ch.SysID = 0, 0
	if dh != ch {
		t.Fatalf("WAL long headers differ structurally beyond SysID: direct %+v, clone %+v", dh, ch)
	}
}

// TestTemplateRefusesWALDirArgs pins the cache refusal for relocated-WAL
// argument sets (a copied template would alias one physical WAL dir).
func TestTemplateRefusesWALDirArgs(t *testing.T) {
	for _, args := range [][]string{
		{"-X", "/tmp/elsewhere"},
		{"--waldir", "/tmp/elsewhere"},
		{"--waldir=/tmp/elsewhere"},
	} {
		if !templateRefused(args) {
			t.Errorf("templateRefused(%v) = false, want true", args)
		}
	}
	if templateRefused([]string{"--data-checksums"}) {
		t.Error("templateRefused(--data-checksums) = true, want false (cacheable variant)")
	}
}

// initDataDirDirect is the direct-init half of initDataDir, exposed for the
// equivalence test: identical arg vector to the template path (--no-sync),
// no cache involved.
func (c *Cluster) initDataDirDirect() error {
	saved := c.syncInit
	c.syncInit = true // force the direct path...
	defer func() { c.syncInit = saved }()
	args := append([]string{"init", "-D", c.dataDir, "--no-sync"}, c.initArgs...)
	res, err := c.runGoopg(args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &initError{res.Stderr}
	}
	return nil
}

type initError struct{ stderr string }

func (e *initError) Error() string { return "init failed: " + e.stderr }

func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = string(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readSysID(t *testing.T, dataDir string) uint64 {
	t.Helper()
	b := readFileBytes(t, filepath.Join(dataDir, "global", "system_identifier"))
	if len(b) != 8 {
		t.Fatalf("system_identifier: %d bytes, want 8", len(b))
	}
	return binary.LittleEndian.Uint64(b)
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
