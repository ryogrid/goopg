package tpch_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// upstreamPG is a minimal lifecycle wrapper around upstream
// PostgreSQL 18.3 from `postgres/local_install/`. Used by the
// result-parity test to compare goopg's Q1..Q22 output against
// authoritative upstream behaviour on the same synthetic dataset.
//
// Lifecycle: initdb → pg_ctl start → query via libpq → pg_ctl stop.
// Each instance runs in a tempdir on a random TCP port so a parity
// run doesn't collide with anything else on the host.
type upstreamPG struct {
	repoRoot string
	dataDir  string
	bin      string // postgres/local_install/bin
	port     int
	user     string
	dbName   string
	logPath  string
}

func newUpstreamPG(t *testing.T, repoRoot, dataDir string) *upstreamPG {
	t.Helper()
	port, err := freeTCPPort()
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	return &upstreamPG{
		repoRoot: repoRoot,
		dataDir:  dataDir,
		bin:      filepath.Join(repoRoot, "postgres", "local_install", "bin"),
		port:     port,
		user:     os.Getenv("USER"),
		dbName:   "postgres",
		logPath:  filepath.Join(dataDir, "..", "upstream.log"),
	}
}

func (p *upstreamPG) initdb() error {
	if _, err := os.Stat(filepath.Join(p.bin, "initdb")); err != nil {
		return fmt.Errorf("upstream PG not installed at %s: %w", p.bin, err)
	}
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
	// Bind only to loopback so we don't open a port to the world.
	pgConf := filepath.Join(p.dataDir, "postgresql.conf")
	if f, err := os.OpenFile(pgConf, os.O_APPEND|os.O_WRONLY, 0); err == nil {
		fmt.Fprintf(f, "\nlisten_addresses = '127.0.0.1'\nport = %d\nfsync = off\nsynchronous_commit = off\n", p.port)
		_ = f.Close()
	}
	return nil
}

func (p *upstreamPG) start() error {
	logFile, err := os.Create(p.logPath)
	if err != nil {
		return err
	}
	_ = logFile.Close()
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-l", p.logPath, "-w",
		"-o", fmt.Sprintf("-p %d -h 127.0.0.1", p.port),
		"start")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl start: %v\n%s\n--- log ---\n%s", err, out, readLogTail(p.logPath))
	}
	return nil
}

func (p *upstreamPG) stop() error {
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-m", "immediate", "-w", "stop")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl stop: %v\n%s", err, out)
	}
	return nil
}

func (p *upstreamPG) query(ctx context.Context, sqlText string) ([][]string, error) {
	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=%s dbname=%s sslmode=disable",
		p.port, p.user, p.dbName)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([][]string, 0)
	for rows.Next() {
		raw := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		line := make([]string, len(cols))
		for i := range raw {
			if raw[i].Valid {
				line[i] = raw[i].String
			}
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(p)
}

func readLogTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 4096 {
		return "...\n" + string(b[len(b)-4096:])
	}
	return string(b)
}

func upstreamPGAvailable(repoRoot string) bool {
	for _, name := range []string{"initdb", "pg_ctl", "postgres", "psql"} {
		if _, err := os.Stat(filepath.Join(repoRoot, "postgres", "local_install", "bin", name)); err != nil {
			return false
		}
	}
	return true
}

func trimRows(rows [][]string) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		cells := make([]string, len(r))
		for j, c := range r {
			cells[j] = strings.TrimSpace(c)
		}
		out[i] = cells
	}
	return out
}

// firstDiff returns the first row index where a and b differ, with
// the cell index, or (-1,-1) when equal. Both inputs are assumed
// already trimmed and re-formatted by the caller.
func firstDiff(a, b [][]string) (int, int) {
	if len(a) != len(b) {
		return -1, -2 // sentinel: row count differs
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return i, -2
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return i, j
			}
		}
	}
	return -1, -1
}

// waitDBReady polls the DSN with `SELECT 1` until it succeeds or
// the deadline expires. pg_ctl -w should already block until ready,
// but redundant guard rails are cheap.
func waitDBReady(ctx context.Context, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return err
		}
		if err := db.PingContext(ctx); err == nil {
			_ = db.Close()
			return nil
		}
		_ = db.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("waitDBReady: timeout after %s", timeout)
}
