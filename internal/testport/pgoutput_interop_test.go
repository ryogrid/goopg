// M0103-0004: pgoutput wire-byte interop verification.
//
// Two subtests, each crossing a goopg ↔ PG boundary:
//
//   (a) TestPort_PgoutputInteropPGToGoopg — goopg's `wal.DecodeMessage`
//       consumes raw pgoutput bytes produced by an upstream PG primary.
//       The producer is `pg_logical_slot_get_binary_changes`, which
//       returns one row per CopyData payload the walsender would have
//       shipped; concatenating the rows yields the exact byte stream a
//       libpq subscriber would consume off the wire. Pure byte-level
//       verification of goopg's decoder against PG's encoder.
//
//   (b) TestPort_PgoutputInteropGoopgToPG — t.Skip pending follow-up
//       work. PG's apply worker requires `CREATE_REPLICATION_SLOT … LOGICAL
//       pgoutput` to succeed on the wire; goopg's `replyCreateReplicationSlot`
//       currently rejects LOGICAL with feature_not_supported (see
//       internal/server/replication.go). Closing that gap is tracked
//       as the remaining M0103-0004(b) work in `.ralph/fix_plan.md`.
//
// See `docs/design/0103-0003-pgoutput-wire-interop.md`.

package testport

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/wal"
)

// freeTCPPort returns an OS-assigned ephemeral port. Race-safe enough
// for tests: the listener is closed immediately, so a subsequent bind
// to the same port is the standard pattern across this codebase.
func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func TestPort_PgoutputInteropPGToGoopg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	pg := newInteropPG(t)
	if pg == nil {
		return // newInteropPG calls t.Skip when binaries are missing
	}
	defer pg.stop()
	if err := pg.start(); err != nil {
		t.Fatalf("pg start: %v", err)
	}

	// Schema + publication. Logical decoding requires REPLICA IDENTITY
	// FULL or a primary key to ship UPDATE/DELETE OldTuple; the PK
	// on `id` satisfies that.
	pg.mustExec(t, `CREATE TABLE t (id int PRIMARY KEY, v text);`)
	pg.mustExec(t, `CREATE PUBLICATION p FOR ALL TABLES;`)

	// Pre-create the logical slot so the changes generated below are
	// captured from this LSN onward. The slot uses pgoutput as the
	// output plugin; the proto_version + publication_names options
	// are passed to pg_logical_slot_get_binary_changes when we drain
	// the slot below.
	pg.mustExec(t, `SELECT pg_create_logical_replication_slot('goopg_interop', 'pgoutput');`)

	// Drive a deterministic set of changes. Each statement is its own
	// transaction, producing four Begin/Commit pairs in the slot.
	pg.mustExec(t, `INSERT INTO t VALUES (1, 'hello');`)
	pg.mustExec(t, `INSERT INTO t VALUES (2, 'world');`)
	pg.mustExec(t, `UPDATE t SET v = 'updated' WHERE id = 2;`)
	pg.mustExec(t, `DELETE FROM t WHERE id = 1;`)

	// Drain the slot's accumulated changes as a single bytea via
	// `pg_logical_slot_get_binary_changes`. This is the in-database
	// equivalent of `pg_recvlogical` writing CopyData payloads to a
	// file, but keeps the test independent of any client tool's
	// buffering / exit-condition semantics. Each row is one pgoutput
	// message; concatenating them gives the same byte stream
	// `pg_recvlogical` would write.
	raw := pg.queryBytea(t, `SELECT string_agg(data, ''::bytea ORDER BY lsn) FROM pg_logical_slot_get_binary_changes('goopg_interop', NULL, NULL, 'proto_version', '1', 'publication_names', 'p');`)
	if len(raw) == 0 {
		slotInfo := pg.queryScalar(t, `SELECT slot_name||':'||restart_lsn::text||':'||confirmed_flush_lsn::text FROM pg_replication_slots WHERE slot_name='goopg_interop';`)
		curLSN := pg.queryScalar(t, `SELECT pg_current_wal_lsn();`)
		t.Fatalf("slot drained 0 bytes; slot=%s cur_lsn=%s", slotInfo, curLSN)
	}

	// `pg_logical_slot_get_binary_changes` returns one row per pgoutput
	// message. Concatenated, the bytea blob is a stream of
	// shape-defined messages: a kind byte followed by per-kind
	// fixed-or-prefix-delimited fields. Walk them in order and route
	// each through goopg's `wal.DecodeMessage`.
	msgs, err := splitPgoutputMessages(raw)
	if err != nil {
		t.Fatalf("split pgoutput bytes: %v\n%x", err, raw)
	}

	// Group into transactions. Expect exactly one B/C pair.
	var (
		begins   int
		commits  int
		relMsgs  int
		inserts  int
		updates  int
		deletes  int
		gotRel   *wal.DecodedRelation
		insertVs []string
		updateVs []string
		deleteVs []string
	)
	for _, payload := range msgs {
		m, err := wal.DecodeMessage(payload)
		if err != nil {
			t.Fatalf("DecodeMessage kind=%q: %v (bytes=%x)", payload[0], err, payload)
		}
		switch m.Kind {
		case 'B':
			begins++
		case 'C':
			commits++
		case 'R':
			relMsgs++
			gotRel = m.Relation
		case 'I':
			inserts++
			insertVs = append(insertVs, tupleSummary(m.NewTuple))
		case 'U':
			updates++
			updateVs = append(updateVs, tupleSummary(m.NewTuple))
		case 'D':
			deletes++
			deleteVs = append(deleteVs, tupleSummary(m.OldTuple))
		}
	}

	if begins < 1 || commits < 1 {
		t.Fatalf("missing Begin/Commit: begins=%d commits=%d", begins, commits)
	}
	if relMsgs < 1 || gotRel == nil {
		t.Fatal("no Relation message decoded")
	}
	if got, want := gotRel.Name, "t"; got != want {
		t.Errorf("rel name=%q want %q", got, want)
	}
	if got, want := len(gotRel.Columns), 2; got != want {
		t.Fatalf("rel ncols=%d want %d", got, want)
	}
	// Column 0: int4 (OID 23). Column 1: text (OID 25).
	if gotRel.Columns[0].TypeOID != 23 {
		t.Errorf("col0 typoid=%d want 23 (int4)", gotRel.Columns[0].TypeOID)
	}
	if gotRel.Columns[1].TypeOID != 25 {
		t.Errorf("col1 typoid=%d want 25 (text)", gotRel.Columns[1].TypeOID)
	}
	if inserts != 2 || updates != 1 || deletes != 1 {
		t.Fatalf("DML counts: inserts=%d updates=%d deletes=%d want 2/1/1",
			inserts, updates, deletes)
	}
	// Cross-check at least one tuple's text value to catch obvious
	// decoder regressions.
	if !containsTuple(insertVs, "1", "hello") || !containsTuple(insertVs, "2", "world") {
		t.Errorf("insert tuple values mismatch: %v", insertVs)
	}
	if !containsTuple(updateVs, "2", "updated") {
		t.Errorf("update new-tuple values mismatch: %v", updateVs)
	}
	// REPLICA IDENTITY DEFAULT: delete carries the 'K' marker (key
	// columns only), so non-key columns come through as NULL.
	if !containsTuple(deleteVs, "1", "<null>") {
		t.Errorf("delete old-tuple values mismatch: %v", deleteVs)
	}
}

func TestPort_PgoutputInteropGoopgToPG(t *testing.T) {
	t.Skip("M0103-0004(b) deferred: requires CREATE_REPLICATION_SLOT LOGICAL " +
		"wire support on goopg (replyCreateReplicationSlot currently rejects " +
		"LOGICAL with feature_not_supported). Tracked in .ralph/fix_plan.md.")
}

// containsTuple looks for an "(a,b)"-shaped summary inside a list.
func containsTuple(have []string, want ...string) bool {
	needle := "(" + strings.Join(want, ",") + ")"
	for _, h := range have {
		if h == needle {
			return true
		}
	}
	return false
}

// tupleSummary renders a `[]wal.DecodedColumn` as "(v0,v1,…)" for
// diagnostic output. NULL → "<null>", unchanged TOAST → "<u>".
func tupleSummary(cols []wal.DecodedColumn) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		switch c.Status {
		case 'n':
			parts[i] = "<null>"
		case 'u':
			parts[i] = "<u>"
		default:
			parts[i] = string(c.Bytes)
		}
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// splitPgoutputMessages walks one or more pgoutput messages packed
// back-to-back inside `buf` and returns each message's bytes (kind +
// payload). Mirrors the per-kind shape table in
// docs/design/0103-0003-pgoutput-wire-interop.md.
//
// We don't fully decode each message here; we only need to know each
// message's length so we can hand a complete payload to
// `wal.DecodeMessage`. For tuple-carrying messages we delegate to a
// tuple-skipping helper.
func splitPgoutputMessages(buf []byte) ([][]byte, error) {
	var out [][]byte
	off := 0
	for off < len(buf) {
		start := off
		kind := buf[off]
		off++
		var err error
		switch kind {
		case 'B':
			off, err = skipExact(buf, off, 20) // final_lsn(8)+commit_ts(8)+xid(4)
		case 'C':
			off, err = skipExact(buf, off, 25) // flags(1)+commit_lsn(8)+end_lsn(8)+commit_ts(8)
		case 'R':
			off, err = skipRelation(buf, off)
		case 'I':
			// rel_oid(4) | 'N' | tuple
			off, err = skipExact(buf, off, 4)
			if err == nil {
				off, err = skipTupleAfterMarker(buf, off, 'N')
			}
		case 'U':
			// rel_oid(4) | ['O'|'K' tuple]? | 'N' tuple
			// (old tuple is omitted entirely under REPLICA IDENTITY
			// DEFAULT when the update doesn't touch key columns)
			off, err = skipExact(buf, off, 4)
			if err == nil {
				if off >= len(buf) {
					return nil, fmt.Errorf("split: truncated 'U'")
				}
				marker := buf[off]
				if marker == 'O' || marker == 'K' {
					off++
					off, err = skipTuple(buf, off)
				}
				if err == nil {
					off, err = skipTupleAfterMarker(buf, off, 'N')
				}
			}
		case 'D':
			// rel_oid(4) | 'O'|'K' tuple
			off, err = skipExact(buf, off, 4)
			if err == nil {
				if off >= len(buf) {
					return nil, fmt.Errorf("split: truncated 'D'")
				}
				marker := buf[off]
				off++
				if marker != 'O' && marker != 'K' {
					return nil, fmt.Errorf("split: 'D' bad marker %q", marker)
				}
				off, err = skipTuple(buf, off)
			}
		case 'T':
			// truncate: nrels(4) + flags(1) + relid[nrels]*4
			if off+5 > len(buf) {
				return nil, fmt.Errorf("split: truncated 'T'")
			}
			nrels := beUint32(buf[off:])
			off += 5
			off, err = skipExact(buf, off, int(nrels)*4)
		case 'M':
			// message: flags(1) + lsn(8) + prefix(cstring) + len(4) + bytes
			off, err = skipExact(buf, off, 9)
			if err == nil {
				off, err = skipCString(buf, off)
			}
			if err == nil {
				if off+4 > len(buf) {
					return nil, fmt.Errorf("split: truncated 'M' len")
				}
				bodyLen := beUint32(buf[off:])
				off += 4
				off, err = skipExact(buf, off, int(bodyLen))
			}
		case 'O':
			// origin: origin_lsn(8) + name(cstring)
			off, err = skipExact(buf, off, 8)
			if err == nil {
				off, err = skipCString(buf, off)
			}
		case 'Y':
			// type: typid(4) + ns(cstring) + name(cstring)
			off, err = skipExact(buf, off, 4)
			if err == nil {
				off, err = skipCString(buf, off)
			}
			if err == nil {
				off, err = skipCString(buf, off)
			}
		default:
			return nil, fmt.Errorf("split: unknown kind %q at offset %d", kind, start)
		}
		if err != nil {
			return nil, fmt.Errorf("split kind=%q at offset %d: %w", kind, start, err)
		}
		out = append(out, buf[start:off])
	}
	return out, nil
}

func skipRelation(buf []byte, off int) (int, error) {
	// rel_oid(4) | ns(cstring) | name(cstring) | replident(1) | ncols(2) |
	//   per-col [ flags(1) | name(cstring) | typoid(4) | typmod(4) ]
	off, err := skipExact(buf, off, 4)
	if err != nil {
		return 0, err
	}
	if off, err = skipCString(buf, off); err != nil {
		return 0, err
	}
	if off, err = skipCString(buf, off); err != nil {
		return 0, err
	}
	if off, err = skipExact(buf, off, 1); err != nil {
		return 0, err
	}
	if off+2 > len(buf) {
		return 0, fmt.Errorf("relation: short ncols")
	}
	ncols := int(beUint16(buf[off:]))
	off += 2
	for i := 0; i < ncols; i++ {
		if off+1 > len(buf) {
			return 0, fmt.Errorf("relation col %d: short flags", i)
		}
		off++
		if off, err = skipCString(buf, off); err != nil {
			return 0, err
		}
		if off, err = skipExact(buf, off, 4); err != nil {
			return 0, err
		}
		if off, err = skipExact(buf, off, 4); err != nil {
			return 0, err
		}
	}
	return off, nil
}

func skipTupleAfterMarker(buf []byte, off int, want byte) (int, error) {
	if off >= len(buf) {
		return 0, fmt.Errorf("tuple: missing marker %q", want)
	}
	if buf[off] != want {
		return 0, fmt.Errorf("tuple: marker=%q want %q", buf[off], want)
	}
	return skipTuple(buf, off+1)
}

func skipTuple(buf []byte, off int) (int, error) {
	if off+2 > len(buf) {
		return 0, fmt.Errorf("tuple: short natts")
	}
	natts := int(beUint16(buf[off:]))
	off += 2
	for i := 0; i < natts; i++ {
		if off >= len(buf) {
			return 0, fmt.Errorf("tuple col %d: short status", i)
		}
		status := buf[off]
		off++
		switch status {
		case 'n', 'u':
			// no body
		case 't', 'b':
			if off+4 > len(buf) {
				return 0, fmt.Errorf("tuple col %d: short length", i)
			}
			ln := int(beUint32(buf[off:]))
			off += 4
			if off+ln > len(buf) {
				return 0, fmt.Errorf("tuple col %d: short body", i)
			}
			off += ln
		default:
			return 0, fmt.Errorf("tuple col %d: bad status %q", i, status)
		}
	}
	return off, nil
}

func skipCString(buf []byte, off int) (int, error) {
	for i := off; i < len(buf); i++ {
		if buf[i] == 0 {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("cstring: unterminated")
}

func skipExact(buf []byte, off, n int) (int, error) {
	if off+n > len(buf) {
		return 0, fmt.Errorf("need %d bytes at %d, have %d", n, off, len(buf)-off)
	}
	return off + n, nil
}

func beUint16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ---- interop-PG harness ----
//
// A small self-contained spawner for upstream PG, modelled on
// internal/testutil/tpch/upstreampg_test.go but inlined here so the
// testport package doesn't take a test-only dependency on tpch. Skip
// when local_install/bin/{initdb,pg_ctl,psql,pg_recvlogical} aren't
// present.

type interopPG struct {
	bin     string
	dataDir string
	logPath string
	port    int
	user    string
	dbName  string
}

func newInteropPG(t *testing.T) *interopPG {
	t.Helper()
	bin := filepath.Join(repoRoot(t), "postgres", "local_install", "bin")
	for _, tool := range []string{"initdb", "pg_ctl", "psql", "pg_recvlogical"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("upstream PG tool %q not found at %s: %v", tool, bin, err)
			return nil
		}
	}
	port, err := freeTCPPort()
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	user := os.Getenv("USER")
	if user == "" {
		user = "postgres"
	}
	dataDir := filepath.Join(t.TempDir(), "pgdata")
	pg := &interopPG{
		bin:     bin,
		dataDir: dataDir,
		logPath: filepath.Join(t.TempDir(), "pg.log"),
		port:    port,
		user:    user,
		dbName:  "postgres",
	}
	if err := pg.initdb(); err != nil {
		t.Fatalf("initdb: %v", err)
	}
	t.Cleanup(func() { _ = pg.stop() })
	return pg
}

func (p *interopPG) initdb() error {
	cmd := exec.Command(filepath.Join(p.bin, "initdb"),
		"-D", p.dataDir,
		"-U", p.user,
		"--auth-local=trust", "--auth-host=trust",
		"--no-sync",
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb: %v\n%s", err, out)
	}
	pgConf := filepath.Join(p.dataDir, "postgresql.conf")
	f, err := os.OpenFile(pgConf, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open postgresql.conf: %w", err)
	}
	defer f.Close()
	// wal_level=logical is required for pgoutput / pg_create_logical_replication_slot.
	// Note: synchronous_commit must be ON so the WAL-written LSN
	// advances synchronously with commits. With synchronous_commit
	// off, pg_logical_slot_*_changes can't see records the WAL
	// writer hasn't flushed yet, even though they're in shared
	// memory.
	fmt.Fprintf(f, "\nlisten_addresses = '127.0.0.1'\nport = %d\nfsync = off\nwal_level = logical\nmax_replication_slots = 4\nmax_wal_senders = 4\n", p.port)
	return nil
}

func (p *interopPG) start() error {
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-l", p.logPath, "-w",
		"-o", fmt.Sprintf("-p %d -h 127.0.0.1", p.port),
		"start")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl start: %v\n%s", err, out)
	}
	return nil
}

func (p *interopPG) stop() error {
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-m", "immediate", "-w", "stop")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	_ = cmd.Run()
	return nil
}

func (p *interopPG) mustExec(t *testing.T, sql string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(p.bin, "psql"),
		"-h", "127.0.0.1", "-p", fmt.Sprintf("%d", p.port),
		"-U", p.user, "-d", p.dbName,
		"-v", "ON_ERROR_STOP=1",
		"-c", sql,
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(filepath.Dir(p.bin), "lib"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql exec %q: %v\n%s", sql, err, out)
	}
}

func (p *interopPG) queryScalar(t *testing.T, sql string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(p.bin, "psql"),
		"-h", "127.0.0.1", "-p", fmt.Sprintf("%d", p.port),
		"-U", p.user, "-d", p.dbName,
		"-tA", "-c", sql,
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C",
		"LD_LIBRARY_PATH="+filepath.Join(filepath.Dir(p.bin), "lib"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql query %q: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// queryBytea reads a single `bytea` column from a one-row query. The
// returned hex-escape (PG18's default `bytea_output = hex`) is decoded
// to raw bytes. Used to slurp pgoutput payloads accumulated in a
// logical slot via `pg_logical_slot_get_binary_changes`.
func (p *interopPG) queryBytea(t *testing.T, sql string) []byte {
	t.Helper()
	s := p.queryScalar(t, sql)
	if s == "" {
		return nil
	}
	if !strings.HasPrefix(s, "\\x") {
		t.Fatalf("queryBytea: expected hex-prefixed bytea, got %q", s[:min(len(s), 32)])
	}
	hexBody := s[2:]
	if len(hexBody)%2 != 0 {
		t.Fatalf("queryBytea: odd hex length %d", len(hexBody))
	}
	out := make([]byte, len(hexBody)/2)
	for i := 0; i < len(out); i++ {
		hi := hexNibble(hexBody[2*i])
		lo := hexNibble(hexBody[2*i+1])
		if hi < 0 || lo < 0 {
			t.Fatalf("queryBytea: bad hex at byte %d", i)
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
