package testport

// Ports of postgres/src/bin/pg_basebackup/t/*.pl tests into Go.
//
// Upstream suites: BB-010, BB-011, BB-020, BB-030, BB-040 in
//   docs/test-port/postgres-oracle-target-inventory.csv.
// Milestone doc: docs/milestones/0095-client-tools-tap-test-porting.md
//
// Each test covers:
//  1. Binary existence check (t.Skip if absent).
//  2. CLI option-validation sub-cases: --help, --version, unknown flag,
//     and mandatory-argument / option-conflict checks that fail before
//     any server connection is attempted.
//  3. t.Skip for the WAL-streaming / physical-replication / logical-replication
//     sub-cases that require pg_basebackup-compatible streaming protocol or a
//     primary + standby cluster — not yet supported in goopg v0.
//
// Binary discovery: PATH first, then postgres/local_install/bin fallback
// (via clientToolBin in client_tools_port_test.go).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgBasebackup010 ports postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - must specify output directory
//   - --compress none:1 fails with "does not accept a compression level"
//   - --compress none+ fails with "unrecognized compression algorithm"
//   - actual backup, WAL-fetching/streaming, incremental, compression, format tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: backup execution sub-cases require pg_basebackup-compatible physical
// streaming protocol which goopg v0 does not expose.
func TestPort_PgBasebackup010(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_basebackup") && !strings.Contains(res.Stderr, "pg_basebackup") {
		t.Fatalf("--help output does not mention pg_basebackup; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// must specify output directory or backup target
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --pgdata should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, "output directory") && !strings.Contains(combined, "backup target") {
		t.Fatalf("expected 'output directory' or 'backup target' in error; got %q", combined)
	}

	// --compress none:1 fails: "none does not accept a compression level"
	res = runTool(t, bin, "--pgdata="+t.TempDir(), "--compress=none:1")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none:1 should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none:1 error; got %q", res.Stdout+res.Stderr)
	}

	// --compress none+ fails: "unrecognized compression algorithm"
	res = runTool(t, bin, "--pgdata="+t.TempDir(), "--compress=none+")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none+ should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none+ error; got %q", res.Stdout+res.Stderr)
	}

	// Backup execution sub-case: now exercised end-to-end via
	// TestPort_PgBasebackup010BackupExecution. See M0102-0002 for the
	// BASE_BACKUP wire-protocol implementation and M0095-0003 for the
	// test-suite progression.
}

// TestPort_PgBasebackup010BackupExecution exercises the upstream
// "actual backup" sub-case of 010_pg_basebackup.pl against a live
// goopg cluster: connect via the replication protocol, issue
// BASE_BACKUP, and unpack the resulting tar into a fresh data
// directory. Verification mirrors upstream's "real backup" assertion
// — backup_label and global/pg_control are present in the extracted
// directory. This test deliberately keeps WAL streaming disabled
// (`-X none`) to isolate the BASE_BACKUP data-copy path; the `-X stream`
// walsender path is covered separately by
// TestPort_PgBasebackup010StreamWAL. Backup manifests remain disabled
// (`--no-manifest`) until `bbsink_manifest` parity ships under an
// M0095-0003 follow-up.
//
// Implementation notes:
//
//   - The cluster.RunClientTool helper requires the binary on $PATH;
//     pg_basebackup ships in postgres/local_install/bin which is not
//     necessarily on the test runner's PATH, so we resolve the
//     absolute path via clientToolBin and invoke it directly with the
//     cluster's listen address.
//
//   - Required server-side GUCs: SHOW data_directory_mode, SHOW
//     wal_segment_size, and SHOW summarize_wal are issued by
//     pg_basebackup before BASE_BACKUP; defaults registered in
//     internal/config/defaults.go.
//
//   - Required wire-protocol parity for BASE_BACKUP: the trailing
//     `CommandComplete("BASE_BACKUP")` emitted by
//     internal/server/basebackup.go matches upstream's
//     EndReplicationCommand wrap so pg_basebackup's final
//     PQgetResult observes PGRES_COMMAND_OK instead of failing with
//     "final receive failed: " (empty error).
func TestPort_PgBasebackup010BackupExecution(t *testing.T) {
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup010_exec")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Seed a small table so the backup includes non-empty heap pages.
	if _, err := c.Query(context.Background(),
		`CREATE TABLE pgbb_seed (a int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO pgbb_seed VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	out := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin,
		"-h", host,
		"-p", port,
		"-U", "postgres",
		"-D", out,
		// -X none avoids the START_REPLICATION + walreceiver path
		// (M0102-0006 follow-up). --no-sync + --no-manifest match the
		// minimum pg_basebackup options that the BASE_BACKUP wire
		// currently supports.
		"-X", "none",
		"--no-sync",
		"--no-manifest",
		"-l", "TestPort_PgBasebackup010BackupExecution")
	cmd.Env = append(os.Environ(), "PGPASSWORD=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_basebackup failed: %v\ncombined output:\n%s", err, string(output))
	}

	for _, rel := range []string{"backup_label", "global/pg_control", "PG_VERSION"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing %s in extracted backup: %v", rel, err)
		}
	}
}

// TestPort_PgBasebackup010StreamWAL exercises the upstream
// "-X stream" sub-case of 010_pg_basebackup.pl: pg_basebackup opens a
// SECOND replication connection alongside the BASE_BACKUP and issues
// START_REPLICATION to stream WAL into the backup's pg_wal/ directory
// concurrently with the data copy. This validates goopg's physical
// walsender loop (internal/server/replication.go replyStartReplication)
// through pg_basebackup's walreceiver, the same protocol M0102 exercises
// for a streaming standby.
//
// Unlike the -X none execution test, a -X stream backup must contain at
// least one streamed WAL segment under pg_wal/ on completion; that is the
// assertion that distinguishes a working stream from a no-op.
func TestPort_PgBasebackup010StreamWAL(t *testing.T) {
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup010_stream")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Seed a small table so the backup includes non-empty heap pages and
	// the WAL stream carries real records.
	if _, err := c.Query(context.Background(),
		`CREATE TABLE pgbb_stream_seed (a int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO pgbb_stream_seed VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	out := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin,
		"-h", host,
		"-p", port,
		"-U", "postgres",
		"-D", out,
		// -X stream forces the START_REPLICATION + walreceiver path: a
		// second replication connection streams WAL into pg_wal/ while the
		// BASE_BACKUP copies the data directory.
		"-X", "stream",
		"--no-sync",
		"--no-manifest",
		"-l", "TestPort_PgBasebackup010StreamWAL")
	cmd.Env = append(os.Environ(), "PGPASSWORD=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_basebackup -X stream failed: %v\ncombined output:\n%s", err, string(output))
	}

	for _, rel := range []string{"backup_label", "global/pg_control", "PG_VERSION"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing %s in extracted backup: %v", rel, err)
		}
	}

	// The defining assertion: -X stream must have streamed at least one WAL
	// segment into the backup's pg_wal/ directory.
	walEntries, err := os.ReadDir(filepath.Join(out, "pg_wal"))
	if err != nil {
		t.Fatalf("read pg_wal in extracted backup: %v", err)
	}
	var segs int
	for _, e := range walEntries {
		// WAL segment file names are 24 hex chars (TLI + logid + segno).
		if !e.IsDir() && len(e.Name()) == 24 {
			segs++
		}
	}
	if segs == 0 {
		var names []string
		for _, e := range walEntries {
			names = append(names, e.Name())
		}
		t.Errorf("expected >=1 streamed WAL segment in pg_wal/, found none; entries=%v", names)
	}
}

// TestPort_PgBasebackup010FetchWAL exercises the upstream "-X fetch"
// (FETCH_WAL) sub-case of 010_pg_basebackup.pl. Unlike -X stream, fetch
// opens NO second replication connection: pg_basebackup sends the
// BASE_BACKUP `WAL` boolean option (pg_basebackup.c:1905-1906) and the
// server must append the in-range pg_wal segments to the backup tar
// itself (basebackup.c:408-560 includewal block). The defining property
// is therefore that the extracted pg_wal/ holds the WAL covering the
// backup's start LSN even though nothing streamed it — it can only have
// come from the server-side tar.
func TestPort_PgBasebackup010FetchWAL(t *testing.T) {
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup010_fetch")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Seed a small table so the backup carries real heap pages and the
	// pre-backup checkpoint has WAL to cover.
	if _, err := c.Query(context.Background(),
		`CREATE TABLE pgbb_fetch_seed (a int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO pgbb_fetch_seed VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	out := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin,
		"-h", host,
		"-p", port,
		"-U", "postgres",
		"-D", out,
		// -X fetch sets the BASE_BACKUP WAL option; the WAL must arrive
		// inside the data tar over the single connection — no walsender.
		"-X", "fetch",
		"--no-sync",
		"--no-manifest",
		"-l", "TestPort_PgBasebackup010FetchWAL")
	cmd.Env = append(os.Environ(), "PGPASSWORD=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_basebackup -X fetch failed: %v\ncombined output:\n%s", err, string(output))
	}

	for _, rel := range []string{"backup_label", "global/pg_control", "PG_VERSION"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing %s in extracted backup: %v", rel, err)
		}
	}

	// The extracted pg_wal/ must hold at least one 24-char WAL segment that
	// the server placed in the tar (fetch streams nothing separately).
	walEntries, err := os.ReadDir(filepath.Join(out, "pg_wal"))
	if err != nil {
		t.Fatalf("read pg_wal in extracted backup: %v", err)
	}
	segNames := map[string]struct{}{}
	for _, e := range walEntries {
		if !e.IsDir() && len(e.Name()) == 24 {
			segNames[e.Name()] = struct{}{}
		}
	}
	if len(segNames) == 0 {
		var names []string
		for _, e := range walEntries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected >=1 fetched WAL segment in pg_wal/, found none; entries=%v", names)
	}

	// Stronger check: the START WAL segment named in backup_label must be
	// among the fetched segments, proving the includewal range covers the
	// backup's consistency point.
	labelBytes, err := os.ReadFile(filepath.Join(out, "backup_label"))
	if err != nil {
		t.Fatalf("read backup_label: %v", err)
	}
	startSeg := parseBackupLabelStartSegment(t, string(labelBytes))
	if _, ok := segNames[startSeg]; !ok {
		var names []string
		for n := range segNames {
			names = append(names, n)
		}
		t.Errorf("START WAL segment %s from backup_label not present among fetched WAL %v", startSeg, names)
	}
}

// parseBackupLabelStartSegment extracts the WAL segment file name from the
// "START WAL LOCATION: X/Y (file ZZZZ...)" line of a backup_label.
func parseBackupLabelStartSegment(t *testing.T, label string) string {
	t.Helper()
	for _, line := range strings.Split(label, "\n") {
		if !strings.HasPrefix(line, "START WAL LOCATION:") {
			continue
		}
		_, after, ok := strings.Cut(line, "(file ")
		if !ok {
			break
		}
		seg, _, ok := strings.Cut(after, ")")
		if !ok {
			break
		}
		return strings.TrimSpace(seg)
	}
	t.Fatalf("could not find START WAL LOCATION file in backup_label:\n%s", label)
	return ""
}

// TestPort_PgBasebackup010Manifest exercises the upstream backup-manifest
// path of 010_pg_basebackup.pl: pg_basebackup run WITHOUT --no-manifest
// requests `MANIFEST 'yes'` (CRC32C by default), and the server must stream
// a PG-version-2 backup manifest after the tar archive. This validates
// goopg's bbsink_manifest emulation (internal/server/basebackup.go
// buildBackupManifest / streamBackupManifest) end-to-end through the real
// pg_basebackup binary, which receives the 'm'/'d' manifest frames and
// writes backup_manifest.
//
// Assertions, strongest first:
//  1. pg_basebackup succeeds with manifests enabled (wire framing correct).
//  2. backup_manifest is well-formed: version 2, lists backup_label and
//     global/pg_control.
//  3. Every Files[] checksum recomputed independently (CRC32C over the
//     extracted file) matches the manifest (no self-referential trust).
//  4. The SHA-256 Manifest-Checksum recomputed over the document prefix
//     matches the trailer.
//  5. If pg_verifybackup is available, `pg_verifybackup -n` accepts the
//     backup (the upstream oracle's own verdict on file-checksum parity).
func TestPort_PgBasebackup010Manifest(t *testing.T) {
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup010_manifest")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Seed a small table so the backup includes non-empty heap pages and
	// the manifest lists real base/<oid>/<relfilenode> files.
	if _, err := c.Query(context.Background(),
		`CREATE TABLE pgbb_manifest_seed (a int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO pgbb_manifest_seed VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	out := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin,
		"-h", host,
		"-p", port,
		"-U", "postgres",
		"-D", out,
		// Default manifest (CRC32C) — note the ABSENCE of --no-manifest.
		"-X", "none",
		"--no-sync",
		"-l", "TestPort_PgBasebackup010Manifest")
	cmd.Env = append(os.Environ(), "PGPASSWORD=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pg_basebackup (manifest) failed: %v\ncombined output:\n%s", err, string(output))
	}

	// 2. backup_manifest exists and parses.
	manifestPath := filepath.Join(out, "backup_manifest")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read backup_manifest: %v", err)
	}
	var manifest struct {
		Version int `json:"PostgreSQL-Backup-Manifest-Version"`
		Files   []struct {
			Path     string `json:"Path"`
			Size     int64  `json:"Size"`
			Algo     string `json:"Checksum-Algorithm"`
			Checksum string `json:"Checksum"`
		} `json:"Files"`
		ManifestChecksum string `json:"Manifest-Checksum"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("backup_manifest is not valid JSON: %v\n%s", err, string(raw))
	}
	if manifest.Version != 2 {
		t.Errorf("manifest version = %d, want 2", manifest.Version)
	}
	byPath := make(map[string]int) // path -> index
	for i, f := range manifest.Files {
		byPath[f.Path] = i
	}
	for _, want := range []string{"backup_label", "global/pg_control"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("manifest Files[] missing %q", want)
		}
	}

	// 3. Independently recompute each file's CRC32C from the extracted
	// backup and compare to the manifest's declared checksum.
	crcTab := crc32.MakeTable(crc32.Castagnoli)
	for _, f := range manifest.Files {
		if f.Algo != "CRC32C" {
			t.Errorf("file %q: Checksum-Algorithm = %q, want CRC32C", f.Path, f.Algo)
			continue
		}
		data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Errorf("manifest lists %q but it is missing on disk: %v", f.Path, err)
			continue
		}
		if int64(len(data)) != f.Size {
			t.Errorf("file %q: size on disk = %d, manifest = %d", f.Path, len(data), f.Size)
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], crc32.Checksum(data, crcTab))
		if got := hex.EncodeToString(b[:]); got != f.Checksum {
			t.Errorf("file %q: CRC32C on disk = %s, manifest = %s", f.Path, got, f.Checksum)
		}
	}

	// 4. Recompute the SHA-256 Manifest-Checksum over the document prefix
	// (everything before the "Manifest-Checksum" field).
	marker := []byte("\"Manifest-Checksum\": \"")
	idx := bytes.LastIndex(raw, marker)
	if idx < 0 {
		t.Fatalf("backup_manifest has no Manifest-Checksum field")
	}
	sum := sha256.Sum256(raw[:idx])
	if got := hex.EncodeToString(sum[:]); got != manifest.ManifestChecksum {
		t.Errorf("Manifest-Checksum = %s, recomputed = %s", manifest.ManifestChecksum, got)
	}

	// 5. Oracle cross-check: pg_verifybackup -n (skip WAL parsing, which
	// needs pg_waldump parity that is out of this increment's scope).
	if vb := clientToolBin(t, "pg_verifybackup"); vb != "" {
		vcmd := exec.Command(vb, "-n", out)
		vout, verr := vcmd.CombinedOutput()
		if verr != nil {
			t.Errorf("pg_verifybackup -n rejected the backup: %v\n%s", verr, string(vout))
		}
	}
}

// TestPort_PgBasebackup010ManifestChecksums exercises the SHA-family
// MANIFEST_CHECKSUMS branches of the backup-manifest emitter
// (internal/server/basebackup.go buildBackupManifest / checksumFile /
// algoName). The default-CRC32C path is covered by
// TestPort_PgBasebackup010Manifest; this test drives pg_basebackup with
// --manifest-checksums=SHA224|SHA256|SHA384|SHA512 so the per-file
// SHA-{224,256,384,512} hash computation, the Checksum-Algorithm JSON
// field, and the (always SHA-256) Manifest-Checksum are each validated
// end-to-end against an independent recomputation AND the upstream
// pg_verifybackup oracle. Mirrors upstream
// postgres/src/bin/pg_basebackup/t/010_pg_basebackup.pl's
// "backup and verify with manifest checksum <algo>" cases.
func TestPort_PgBasebackup010ManifestChecksums(t *testing.T) {
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup010_manifest_checksums")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Seed a small table so the backup includes non-empty heap pages and
	// the manifest lists real base/<oid>/<relfilenode> files to checksum.
	if _, err := c.Query(context.Background(),
		`CREATE TABLE pgbb_mc_seed (a int)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := c.Query(context.Background(),
		`INSERT INTO pgbb_mc_seed VALUES (1), (2), (3)`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	// recompute returns the lowercase-hex per-file checksum for the given
	// algorithm, matching manifestChecksumKind.checksumFile in the server.
	recompute := func(algo string, data []byte) string {
		switch algo {
		case "SHA224":
			sum := sha256.Sum224(data)
			return hex.EncodeToString(sum[:])
		case "SHA256":
			sum := sha256.Sum256(data)
			return hex.EncodeToString(sum[:])
		case "SHA384":
			sum := sha512.Sum384(data)
			return hex.EncodeToString(sum[:])
		case "SHA512":
			sum := sha512.Sum512(data)
			return hex.EncodeToString(sum[:])
		default:
			t.Fatalf("unhandled algo %q", algo)
			return ""
		}
	}

	for _, algo := range []string{"SHA224", "SHA256", "SHA384", "SHA512"} {
		t.Run(algo, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "backup")
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			cmd := exec.Command(bin,
				"-h", host,
				"-p", port,
				"-U", "postgres",
				"-D", out,
				"--manifest-checksums="+algo,
				"-X", "none",
				"--no-sync",
				"-l", "TestPort_PgBasebackup010ManifestChecksums_"+algo)
			cmd.Env = append(os.Environ(), "PGPASSWORD=")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("pg_basebackup --manifest-checksums=%s failed: %v\ncombined output:\n%s",
					algo, err, string(output))
			}

			raw, err := os.ReadFile(filepath.Join(out, "backup_manifest"))
			if err != nil {
				t.Fatalf("read backup_manifest: %v", err)
			}
			var manifest struct {
				Version int `json:"PostgreSQL-Backup-Manifest-Version"`
				Files   []struct {
					Path     string `json:"Path"`
					Size     int64  `json:"Size"`
					Algo     string `json:"Checksum-Algorithm"`
					Checksum string `json:"Checksum"`
				} `json:"Files"`
				ManifestChecksum string `json:"Manifest-Checksum"`
			}
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatalf("backup_manifest is not valid JSON: %v\n%s", err, string(raw))
			}
			if manifest.Version != 2 {
				t.Errorf("manifest version = %d, want 2", manifest.Version)
			}
			if len(manifest.Files) == 0 {
				t.Fatalf("manifest Files[] is empty")
			}

			// Every file must use the requested algorithm and its declared
			// checksum must match an independent recomputation from disk.
			for _, f := range manifest.Files {
				if f.Algo != algo {
					t.Errorf("file %q: Checksum-Algorithm = %q, want %q", f.Path, f.Algo, algo)
					continue
				}
				data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(f.Path)))
				if err != nil {
					t.Errorf("manifest lists %q but it is missing on disk: %v", f.Path, err)
					continue
				}
				if int64(len(data)) != f.Size {
					t.Errorf("file %q: size on disk = %d, manifest = %d", f.Path, len(data), f.Size)
				}
				if got := recompute(algo, data); got != f.Checksum {
					t.Errorf("file %q: %s on disk = %s, manifest = %s", f.Path, algo, got, f.Checksum)
				}
			}

			// The Manifest-Checksum is always SHA-256 over the document
			// prefix, regardless of the per-file checksum algorithm
			// (upstream backup_manifest.c AddWALInfoToBackupManifest /
			// SendBackupManifest always finalises with PG_SHA256).
			marker := []byte("\"Manifest-Checksum\": \"")
			idx := bytes.LastIndex(raw, marker)
			if idx < 0 {
				t.Fatalf("backup_manifest has no Manifest-Checksum field")
			}
			sum := sha256.Sum256(raw[:idx])
			if got := hex.EncodeToString(sum[:]); got != manifest.ManifestChecksum {
				t.Errorf("Manifest-Checksum = %s, recomputed (SHA256) = %s", manifest.ManifestChecksum, got)
			}

			// Oracle cross-check: pg_verifybackup -n must accept the backup,
			// independently validating every per-file SHA checksum and the
			// manifest checksum against the on-disk files.
			if vb := clientToolBin(t, "pg_verifybackup"); vb != "" {
				vcmd := exec.Command(vb, "-n", out)
				vout, verr := vcmd.CombinedOutput()
				if verr != nil {
					t.Errorf("pg_verifybackup -n rejected the %s backup: %v\n%s", algo, verr, string(vout))
				}
			}
		})
	}
}

// TestPort_PgBasebackup011InPlaceTablespace ports
// postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl.
//
// Upstream test: create an in-place tablespace
// (`SET allow_in_place_tablespaces = on; CREATE TABLESPACE inplace LOCATION ”`),
// back the cluster up in tar format with --wal-method none, and assert the
// backup contains base.tar plus exactly one per-tablespace `<oid>.tar`.
//
// LANDED (2026-06-15, loop #13): the in-place tablespace feature shipped, so
// this test now runs the real upstream scenario. The three prerequisites are
// all in place:
//  1. the `allow_in_place_tablespaces` GUC (config/defaults.go),
//  2. `CREATE TABLESPACE <name> LOCATION ”` DDL creating an in-place
//     pg_tblspc/<oid>/PG_<major>_<catversion> directory
//     (parser/ddl.go + executor/operators_ddl.go execCreateTablespace),
//  3. BASE_BACKUP emitting each non-default tablespace as a separate
//     `<oid>.tar` member plus a tablespace-list row
//     (internal/server/basebackup.go: collectInPlaceTablespaces /
//     emitTablespaceTar / writeTablespaceList).
func TestPort_PgBasebackup011InPlaceTablespace(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not in PATH or postgres/local_install/bin")
	}
	c := newDurableCluster(t, "pgbasebackup011_inplace")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}

	// Create an in-place tablespace. allow_in_place_tablespaces is PGC_SUSET
	// and the SET + CREATE must share a single session, so run both through one
	// `psql -c` simple-query (mirrors upstream's safe_psql heredoc). c.Query
	// uses the extended protocol (one statement per call) and would not carry
	// the SET into the CREATE.
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")
	psqlCmd := exec.Command(psqlBin,
		"-h", host, "-p", port, "-U", "postgres",
		"-v", "ON_ERROR_STOP=1",
		"-c", "SET allow_in_place_tablespaces = on; CREATE TABLESPACE inplace LOCATION ''")
	psqlCmd.Env = append(os.Environ(), "PGPASSWORD=", "LD_LIBRARY_PATH="+libDir)
	if out, err := psqlCmd.CombinedOutput(); err != nil {
		t.Fatalf("create in-place tablespace failed: %v\noutput:\n%s", err, string(out))
	}

	// Back it up in tar format with no WAL (upstream 011's invocation).
	out := filepath.Join(t.TempDir(), "backup")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command(bin,
		"-h", host, "-p", port, "-U", "postgres",
		"-D", out,
		"--format", "tar",
		"--wal-method", "none",
		"--no-sync")
	cmd.Env = append(os.Environ(), "PGPASSWORD=", "LD_LIBRARY_PATH="+libDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_basebackup failed: %v\ncombined output:\n%s", err, string(output))
	}

	// base.tar must exist (the main data directory archive).
	if _, err := os.Stat(filepath.Join(out, "base.tar")); err != nil {
		t.Errorf("missing base.tar in backup: %v", err)
	}

	// Exactly one numeric-named <oid>.tar tablespace archive must exist —
	// the upstream `glob "$backupdir/[0-9]*.tar"` assertion.
	matches, err := filepath.Glob(filepath.Join(out, "[0-9]*.tar"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected exactly one tablespace tar, found %d: %v", len(matches), matches)
	}
}

// TestPort_PgReceivewal020 ports postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - needs target directory
//   - --create-slot + --drop-slot conflict
//   - --create-slot without --slot name
//   - --synchronous + --no-sync conflict
//   - --compress none:1 fails with "does not accept a compression level"
//   - slot creation, WAL streaming, compression, partial-segment, synchronous tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: WAL streaming and slot management require replication protocol.
func TestPort_PgReceivewal020(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/020_pg_receivewal.pl
	bin := clientToolBin(t, "pg_receivewal")
	if bin == "" {
		t.Skip("pg_receivewal not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_receivewal") && !strings.Contains(res.Stderr, "pg_receivewal") {
		t.Fatalf("--help output does not mention pg_receivewal; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	streamDir := t.TempDir()

	// needs target directory
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --directory should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "target directory") && !strings.Contains(res.Stdout+res.Stderr, "directory") {
		t.Fatalf("expected 'target directory' in error; got %q", res.Stdout+res.Stderr)
	}

	// --create-slot and --drop-slot are mutually exclusive
	res = runTool(t, bin, "--directory="+streamDir, "--create-slot", "--drop-slot")
	if res.ExitCode == 0 {
		t.Fatalf("--create-slot + --drop-slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// --create-slot requires --slot
	res = runTool(t, bin, "--directory="+streamDir, "--create-slot")
	if res.ExitCode == 0 {
		t.Fatalf("--create-slot without --slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "slot") {
		t.Fatalf("expected 'slot' in --create-slot error; got %q", res.Stdout+res.Stderr)
	}

	// --synchronous and --no-sync are mutually exclusive
	res = runTool(t, bin, "--directory="+streamDir, "--synchronous", "--no-sync")
	if res.ExitCode == 0 {
		t.Fatalf("--synchronous + --no-sync should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// --compress none:1 fails
	res = runTool(t, bin, "--directory="+streamDir, "--compress=none:1")
	if res.ExitCode == 0 {
		t.Fatalf("--compress=none:1 should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "none") {
		t.Fatalf("expected 'none' in --compress=none:1 error; got %q", res.Stdout+res.Stderr)
	}

	// ----------------------------------------------------------------------
	// Slot-management + WAL-streaming execution tier.
	//
	// Previously deferred ("goopg v0 does not implement the pg_receivewal
	// streaming replication protocol"). goopg now exposes the full physical
	// walsender path pg_receivewal drives — IDENTIFY_SYSTEM,
	// CREATE_REPLICATION_SLOT name PHYSICAL [RESERVE_WAL], START_REPLICATION,
	// DROP_REPLICATION_SLOT, and the pg_replication_slots view
	// (internal/server/replication.go) — the same protocol the working
	// TestPort_PgBasebackup010StreamWAL `-X stream` test exercises. This tier
	// reproduces upstream 020's slot create -> stream -> drop sequence against
	// a live goopg cluster.
	c := newDurableCluster(t, "pgreceivewal020")
	if err := c.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	connArgs := []string{"-h", host, "-p", port, "-U", "postgres"}
	const slotName = "pgrecvwal_test"

	// Upstream: command_ok([pg_receivewal --slot test --create-slot]) creating
	// a replication slot. pg_receivewal sends CREATE_REPLICATION_SLOT ... PHYSICAL.
	createArgs := append([]string{"--slot=" + slotName, "--create-slot", "--if-not-exists",
		"--directory=" + streamDir}, connArgs...)
	res = runTool(t, bin, createArgs...)
	if res.ExitCode != 0 {
		t.Fatalf("--create-slot exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}

	// Upstream: $primary->slot($slot_name) — the slot is now visible in
	// pg_replication_slots as a physical slot.
	rows, err := c.Query(context.Background(),
		"SELECT slot_type FROM pg_replication_slots WHERE slot_name = '"+slotName+"'")
	if err != nil {
		t.Fatalf("query pg_replication_slots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for slot %q in pg_replication_slots, got %d (%v)", slotName, len(rows), rows)
	}
	if got := rows[0][0]; got != "physical" {
		t.Errorf("slot_type = %q, want %q", got, "physical")
	}

	// Stream WAL into a fresh directory via the slot. pg_receivewal runs until
	// killed; it opens the current segment as <name>.partial as soon as it
	// starts receiving WAL.
	recvDir := t.TempDir()
	streamArgs := append([]string{"--slot=" + slotName, "--directory=" + recvDir, "--no-sync", "--verbose"}, connArgs...)
	stream := exec.Command(bin, streamArgs...)
	stream.Env = append(os.Environ(), "PGPASSWORD=")
	var streamErr bytes.Buffer
	stream.Stdout = &streamErr
	stream.Stderr = &streamErr
	if err := stream.Start(); err != nil {
		t.Fatalf("start pg_receivewal stream: %v", err)
	}
	streamKilled := false
	killStream := func() {
		if !streamKilled {
			_ = stream.Process.Kill()
			_ = stream.Wait()
			streamKilled = true
		}
	}
	defer killStream()

	// Generate WAL while polling for a streamed segment to appear. The walfile
	// is created once pg_receivewal receives the first records. (lib/pq's
	// extended protocol rejects multi-statement strings, so each statement is
	// issued separately.)
	if _, err := c.Query(context.Background(), "CREATE TABLE IF NOT EXISTS pgrecvwal_seed (a int)"); err != nil {
		t.Fatalf("create seed table: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	var streamed string
	for time.Now().Before(deadline) {
		_, _ = c.Query(context.Background(), "INSERT INTO pgrecvwal_seed VALUES (1),(2),(3)")
		entries, _ := os.ReadDir(recvDir)
		for _, e := range entries {
			name := e.Name()
			base := strings.TrimSuffix(name, ".partial")
			// WAL segment file names are 24 hex chars (TLI + logid + segno).
			if !e.IsDir() && len(base) == 24 && isHex(base) {
				streamed = name
				break
			}
		}
		if streamed != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if streamed == "" {
		entries, _ := os.ReadDir(recvDir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		killStream()
		t.Fatalf("pg_receivewal streamed no WAL segment within timeout; dir=%v\npg_receivewal output:\n%s",
			names, streamErr.String())
	}
	killStream()

	// Upstream: command_ok([pg_receivewal --slot test --drop-slot]) dropping
	// the replication slot via DROP_REPLICATION_SLOT.
	dropArgs := append([]string{"--slot=" + slotName, "--drop-slot"}, connArgs...)
	res = runTool(t, bin, dropArgs...)
	if res.ExitCode != 0 {
		t.Fatalf("--drop-slot exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
	rows, err = c.Query(context.Background(),
		"SELECT count(*) FROM pg_replication_slots WHERE slot_name = '"+slotName+"'")
	if err != nil {
		t.Fatalf("query pg_replication_slots after drop: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "0" {
		t.Errorf("slot %q still present after --drop-slot: %v", slotName, rows)
	}
}

// isHex reports whether s consists solely of hexadecimal digits.
func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// TestPort_PgRecvlogical030 ports postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - needs a slot name
//   - needs a database
//   - needs an action
//   - no destination file specified (when --start given)
//   - logical slot creation, logical decoding, streaming, plugin tests
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: logical replication streaming and slot management require replication protocol.
func TestPort_PgRecvlogical030(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/030_pg_recvlogical.pl
	bin := clientToolBin(t, "pg_recvlogical")
	if bin == "" {
		t.Skip("pg_recvlogical not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_recvlogical") && !strings.Contains(res.Stderr, "pg_recvlogical") {
		t.Fatalf("--help output does not mention pg_recvlogical; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	// no slot specified
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --slot should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "slot") {
		t.Fatalf("expected 'slot' in no-slot error; got %q", res.Stdout+res.Stderr)
	}

	// no database specified
	res = runTool(t, bin, "--slot=test")
	if res.ExitCode == 0 {
		t.Fatalf("no --dbname should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "database") {
		t.Fatalf("expected 'database' in no-dbname error; got %q", res.Stdout+res.Stderr)
	}

	// no action specified
	res = runTool(t, bin, "--slot=test", "--dbname=postgres")
	if res.ExitCode == 0 {
		t.Fatalf("no action should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "action") {
		t.Fatalf("expected 'action' in no-action error; got %q", res.Stdout+res.Stderr)
	}

	// no destination file (--start without --file/-f)
	res = runTool(t, bin, "--slot=test", "--dbname=postgres", "--start")
	if res.ExitCode == 0 {
		t.Fatalf("--start without file should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	combined := res.Stdout + res.Stderr
	if !strings.Contains(combined, "file") && !strings.Contains(combined, "target") {
		t.Fatalf("expected 'file' or 'target' in no-file error; got %q", combined)
	}

	// Logical decoding, slot creation/drop, and streaming sub-cases deferred:
	// goopg v0 does not implement pg_recvlogical logical replication streaming.
	// Remove this Skip when goopg supports CREATE_REPLICATION_SLOT + logical decoding.
	t.Skip("pg_recvlogical logical streaming and slot management require logical " +
		"replication protocol not yet fully supported for pg_recvlogical in goopg v0")
}

// TestPort_PgCreatesubscriber040 ports
// postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl.
//
// Upstream tests:
//   - program_help_ok / program_version_ok / program_options_handling_ok
//   - no subscriber data directory specified
//   - no publisher connection string specified
//   - no database name specified
//   - actual subscriber setup (requires running primary + standby cluster)
//
// Adapted: CLI and option-validation sub-cases pass.
// Deferred: subscriber setup requires physical streaming + logical replication.
func TestPort_PgCreatesubscriber040(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/040_pg_createsubscriber.pl
	bin := clientToolBin(t, "pg_createsubscriber")
	if bin == "" {
		t.Skip("pg_createsubscriber not in PATH or postgres/local_install/bin")
	}

	// program_help_ok
	res := runTool(t, bin, "--help")
	if res.ExitCode != 0 {
		t.Fatalf("--help exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "pg_createsubscriber") && !strings.Contains(res.Stderr, "pg_createsubscriber") {
		t.Fatalf("--help output does not mention pg_createsubscriber; stdout=%q", res.Stdout)
	}

	// program_version_ok
	res = runTool(t, bin, "--version")
	if res.ExitCode != 0 {
		t.Fatalf("--version exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	// program_options_handling_ok
	res = runTool(t, bin, "--unknown-option-xyz")
	if res.ExitCode == 0 {
		t.Fatalf("--unknown-option-xyz should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}

	tmpDir := t.TempDir()

	// no subscriber data directory specified
	res = runTool(t, bin)
	if res.ExitCode == 0 {
		t.Fatalf("no --pgdata should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "data directory") && !strings.Contains(res.Stdout+res.Stderr, "subscriber") {
		t.Fatalf("expected 'data directory' or 'subscriber' in no-pgdata error; got %q",
			res.Stdout+res.Stderr)
	}

	// no publisher connection string specified
	res = runTool(t, bin, "--pgdata="+tmpDir)
	if res.ExitCode == 0 {
		t.Fatalf("no --publisher-server should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "publisher") {
		t.Fatalf("expected 'publisher' in no-publisher-server error; got %q",
			res.Stdout+res.Stderr)
	}

	// no database name specified
	res = runTool(t, bin, "--verbose", "--pgdata="+tmpDir, "--publisher-server=port=5432")
	if res.ExitCode == 0 {
		t.Fatalf("no --database should exit non-0; stdout=%q stderr=%q",
			res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout+res.Stderr, "database") {
		t.Fatalf("expected 'database' in no-database error; got %q",
			res.Stdout+res.Stderr)
	}

	// Subscriber setup sub-cases deferred: requires a running primary + standby
	// with physical streaming + logical replication, not yet supported in goopg v0.
	// Remove this Skip when goopg supports pg_createsubscriber-compatible protocol.
	t.Skip("pg_createsubscriber subscriber setup requires physical streaming + " +
		"logical replication not yet supported in goopg v0")
}
