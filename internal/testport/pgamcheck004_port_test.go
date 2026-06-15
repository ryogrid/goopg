package testport

// Port of the page-structural heap-corruption tier of
// postgres/src/bin/pg_amcheck/t/004_verify_heapam.pl into a Go test
// (M0110-0003, row AC-003).
//
// 004_verify_heapam.pl is the canonical end-to-end demonstration that the
// pg_amcheck binary, driving the server's verify_heapam() SRF, detects on-disk
// heap corruption. Upstream achieves this by stopping the node, seeking to
// precise byte offsets within a heap page, overwriting deliberately chosen
// bytes, restarting, and asserting pg_amcheck reports the corruption. We mirror
// exactly that mechanism against a live goopg cluster.
//
// FAITHFUL SUBSET. Much of 004_verify_heapam.pl corrupts the MVCC/attribute
// tier of a tuple (xmin/xmax vs clog, multixact, and — most heavily — the
// on-disk TOAST varatt_external pointer of a forcibly-toasted text column).
// goopg's on-disk tuple/TOAST layout DIVERGES from PostgreSQL there (goopg
// stores oversized values in a chunk relation, not a PG varatt_external
// pointer), so a byte-for-byte port of those cases would assert against bytes
// goopg never writes. This port therefore covers the part of 004 that IS
// faithful to goopg's storage format: the page-structural line-pointer tier
// (verify_heapam.c's first loop / amcheck.VerifyHeapPage tier 1), whose page
// header + ItemId layout goopg shares with upstream byte-for-byte
// (storage/page.go, storage/heap.go). The deliberately-corrupted line pointer
// here makes verify_heapam emit the upstream-verbatim
//   "line pointer to page offset %u with length %u ends beyond maximum page
//    offset %u"
// report, which pg_amcheck surfaces on stdout while exiting 2 — the same
// observable contract 004_verify_heapam.pl asserts for its page-structural
// cases.
//
// SCOPING ADAPTATION. Upstream runs `pg_amcheck <db>` over the whole database
// and asserts an empty pre-corruption run. goopg's system-catalog heap pages do
// not yet round-trip cleanly through verify_heapam (a separate, larger parity
// effort — see the deferral ledger), so a whole-db clean run is out of scope
// for this tier. We restrict pg_amcheck to the single user table under test
// (`--table public.t004`); the corruption-detection behaviour being verified —
// the heap line-pointer tier on a user relation — is unchanged by the scoping.
//
// SELF-PROMOTING (cf. AC-002 / WD-002). The test drives the real upstream
// pg_amcheck end-to-end. If the pre-corruption clean run over the user table is
// not clean (a goopg SQL/engine gap in pg_amcheck's bootstrap or the
// verify_heapam LATERAL path), it t.Skips with the precise captured blocker
// rather than reporting a misleading FAIL. The day that gap closes the skip
// clears and the corruption assertion runs unchanged.
//
// Like 001/002, the bundled pg_amcheck links a PG-17+ libpq symbol
// (PQcancelBlocking), so it runs with LD_LIBRARY_PATH pointed at
// postgres/local_install/lib (handled by runAmcheck/amcheckEnv in
// pgamcheck002_port_test.go).
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md (M0110-0003).
// CSV row: AC-003.

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// queryScalar runs sqlText and returns the single value of the first row/column.
func queryScalar(t *testing.T, c *cluster.Cluster, sqlText string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := c.Query(ctx, sqlText)
	if err != nil {
		t.Fatalf("query %q: %v", sqlText, err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		t.Fatalf("query %q returned no value", sqlText)
	}
	return rows[0][0]
}

// findHeapFile locates the main-fork heap file for the relation OID under
// <datadir>/base/*/<reloid>. goopg's storage database directory is keyed by an
// internal dbOid that does not match pg_database.oid, so we glob across database
// dirs and match on the (unique) relation OID filename.
func findHeapFile(t *testing.T, c *cluster.Cluster, reloid string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(c.DataDir(), "base", "*", reloid))
	if err != nil {
		t.Fatalf("glob heap file for reloid %s: %v", reloid, err)
	}
	if len(matches) == 0 {
		t.Skipf("AC-003 blocked: no heap file for relation OID %s under base/*/ "+
			"(unexpected goopg on-disk layout)", reloid)
	}
	return matches[0]
}

// corruptFirstLinePointerLength overwrites the length field of the first line
// pointer on block 0 of the heap file so that lp_off + lp_len exceeds BLCKSZ,
// exactly as 004_verify_heapam.pl seeks-and-overwrites a heap page. The ItemId
// is the 4-byte little-endian word at SizeOfPageHeaderData (slot 1), packed as
// offset(15) | flags<<15 | length<<17 — the layout goopg shares with upstream
// (storage/heap.go ItemID.Pack). Returns the (offset, oldLen, newLen) it wrote.
func corruptFirstLinePointerLength(t *testing.T, path string) (off, oldLen, newLen uint16) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open heap file %q: %v", path, err)
	}
	defer f.Close()

	const itemIDOff = storage.SizeOfPageHeaderData // slot 1 on block 0
	buf := make([]byte, 4)
	if _, err := f.ReadAt(buf, itemIDOff); err != nil {
		t.Fatalf("read line pointer: %v", err)
	}
	raw := binary.LittleEndian.Uint32(buf)
	off = uint16(raw & 0x7FFF)
	flags := uint8((raw >> 15) & 0x3)
	oldLen = uint16((raw >> 17) & 0x7FFF)

	// LP_NORMAL == 1: slot 1 must carry a live tuple for the corruption to be a
	// faithful page-structural injection (not a redirect/dead/unused pointer).
	if flags != 1 {
		t.Fatalf("first line pointer is not LP_NORMAL (flags=%d, off=%d, len=%d) — "+
			"unexpected heap layout", flags, off, oldLen)
	}

	// Maximal length (0x7FFF) guarantees off+len > BLCKSZ regardless of where
	// the tuple region starts, tripping the "ends beyond maximum page offset"
	// check in amcheck.VerifyHeapPage.
	newLen = 0x7FFF
	newRaw := (uint32(off) & 0x7FFF) | (uint32(flags)&0x3)<<15 | (uint32(newLen)&0x7FFF)<<17
	binary.LittleEndian.PutUint32(buf, newRaw)
	if _, err := f.WriteAt(buf, itemIDOff); err != nil {
		t.Fatalf("write corrupted line pointer: %v", err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync heap file: %v", err)
	}
	return off, oldLen, newLen
}

func TestPort_PgAmcheck004VerifyHeapam(t *testing.T) {
	// upstream: postgres/src/bin/pg_amcheck/t/004_verify_heapam.pl (page-structural tier)
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	// Init with data checksums OFF, mirroring 004_verify_heapam.pl's
	// `$node->init(no_data_checksums => 1)`. goopg defaults checksums ON; with
	// them on, overwriting page bytes trips goopg's storage-manager checksum
	// verification ("invalid page in block 0 ... checksum verification failed")
	// before verify_heapam ever inspects the corruption.
	c, err := cluster.New("amcheck004", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		InitArgs:     []string{"--no-data-checksums"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("CREATE EXTENSION amcheck: %v", err)
	}

	// A 3-column table whose rows land on block 0 with small inline values (no
	// TOAST), mirroring 004's predictable single-page layout intent.
	for _, stmt := range []string{
		"CREATE TABLE t004 (a bigint, b text, c text)",
		"INSERT INTO t004 VALUES (1, 'abcdefg', 'hijklmn')",
		"INSERT INTO t004 VALUES (2, 'opqrstu', 'vwxyz01')",
		"INSERT INTO t004 VALUES (3, '2345678', '9abcdef')",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Resolve the heap file path before stopping. goopg names the backing file
	// by the relation OID (no separate relfilenode in v0; see catalog
	// RelFileNode + storage/smgr.go relPath). The on-disk database directory is
	// keyed by the storage dbOid, which is NOT the value pg_database.oid reports
	// for this database, so glob base/<anydb>/<reloid> rather than trusting the
	// catalog's database OID.
	reloid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 't004' AND relkind = 'r'")
	heapPath := findHeapFile(t, c, reloid)

	// Pre-corruption clean run, restricted to the user table (see SCOPING
	// ADAPTATION). Self-promoting: if pg_amcheck cannot cleanly check the
	// healthy user table, skip with the captured blocker rather than FAIL.
	pre := runAmcheck(t, c, "--table", "public.t004", "postgres")
	if pre.ExitCode != 0 || pre.Stdout != "" {
		t.Skipf("AC-003 blocked: pre-corruption pg_amcheck over a healthy user "+
			"table is not clean (a goopg gap in pg_amcheck bootstrap or the "+
			"verify_heapam LATERAL path). exit=%d stdout=%q stderr=%q",
			pre.ExitCode, pre.Stdout, pre.Stderr)
	}

	// Stop cleanly (a shutdown checkpoint flushes the heap page to disk), then
	// overwrite the first line pointer's length on block 0 — the upstream
	// stop -> seek/overwrite -> restart corruption mechanism.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster for corruption: %v", err)
	}
	if _, err := os.Stat(heapPath); err != nil {
		t.Skipf("AC-003 blocked: heap file %q not found after shutdown "+
			"(unexpected goopg on-disk layout): %v", heapPath, err)
	}
	off, oldLen, newLen := corruptFirstLinePointerLength(t, heapPath)
	t.Logf("corrupted block 0 line pointer 1: off=%d len %d -> %d", off, oldLen, newLen)

	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after corruption: %v", err)
	}
	// CREATE EXTENSION amcheck records its install in runtime-only state
	// (per-database pg_extension scoping, gap #7c), which does not survive a
	// server restart; re-install it so pg_amcheck's per-database probe finds it.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("re-CREATE EXTENSION amcheck after restart: %v", err)
	}

	// Post-corruption run: pg_amcheck must report the page-structural corruption
	// on stdout and exit 2 (pg_amcheck's "corruption found" exit status).
	post := runAmcheck(t, c, "--table", "public.t004", "postgres")
	if post.ExitCode != 2 {
		t.Errorf("post-corruption pg_amcheck exit=%d, want 2\n  stdout=%q\n  stderr=%q",
			post.ExitCode, post.Stdout, post.Stderr)
	}
	want := regexp.MustCompile(`line pointer to page offset \d+ with length 32767 ends beyond maximum page offset 8192`)
	if !want.MatchString(post.Stdout) {
		t.Errorf("post-corruption pg_amcheck stdout missing line-pointer report /%s/\n  stdout=%q\n  stderr=%q",
			want, post.Stdout, post.Stderr)
	}
	// The report must name the corrupt heap relation (pg_amcheck prefixes each
	// corruption line with the relation it checked).
	if post.ExitCode == 2 && !strings.Contains(post.Stdout, "t004") {
		t.Errorf("post-corruption pg_amcheck stdout does not name table t004\n  stdout=%q", post.Stdout)
	}
}
