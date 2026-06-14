package testport

// Ports of postgres/src/bin/pg_basebackup/t/*.pl tests into Go.
//
// Upstream suites: BB-010, BB-011, BB-020, BB-030, BB-040 in
//   docs/test-port/postgres-oracle-port-status.csv.
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
	c := newCluster(t, "pgbasebackup010_exec")
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
	c := newCluster(t, "pgbasebackup010_stream")
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
	c := newCluster(t, "pgbasebackup010_manifest")
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
	c := newCluster(t, "pgbasebackup010_manifest_checksums")
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
// Upstream tests: backup of a cluster containing an in-place tablespace.
// All sub-cases require a running primary with pg_basebackup physical streaming.
//
// Deferred entirely: in-place tablespace backup requires physical streaming
// replication (--wal-method none still needs BASE_BACKUP protocol) which is
// not yet implemented in goopg v0.
func TestPort_PgBasebackup011InPlaceTablespace(t *testing.T) {
	// upstream: postgres/src/bin/pg_basebackup/t/011_in_place_tablespace.pl
	bin := clientToolBin(t, "pg_basebackup")
	if bin == "" {
		t.Skip("pg_basebackup not in PATH or postgres/local_install/bin")
	}

	// All sub-cases require BASE_BACKUP physical streaming.
	// Deferred until goopg implements pg_basebackup-compatible replication protocol.
	t.Skip("in-place tablespace backup requires physical streaming replication " +
		"(BASE_BACKUP protocol) not yet implemented in goopg v0")
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

	// WAL streaming, slot creation/drop, and compression sub-cases deferred:
	// goopg v0 does not implement the pg_receivewal streaming replication protocol.
	// Remove this Skip when goopg supports START_REPLICATION / WAL receiver protocol.
	t.Skip("pg_receivewal streaming and slot management require replication protocol " +
		"not yet implemented in goopg v0")
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
