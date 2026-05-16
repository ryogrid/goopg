// Package pgcluster spawns an upstream PostgreSQL cluster from
// `./postgres/local_install/bin` so heterogeneous tests can drive
// real PG alongside goopg. Mirrors the API shape of
// `internal/testutil/cluster` (the goopg cluster harness) so the same
// vocabulary covers either binary in `internal/testutil/pubsubcluster`.
//
// The cluster is self-contained: initdb under a fresh tempdir,
// pg_ctl start/stop, psql for queries. Auth is trust on a 127.0.0.1
// listener picked from an ephemeral port. WalLevel defaults to
// `logical` so the package serves as the publisher/subscriber side
// of the M0103 logical-replication harness without further wiring.
//
// Tests that import this package MUST call `Available(t)` first; the
// helper skips when the upstream binaries are absent (i.e. on a
// developer machine without `make local-install`).
package pgcluster

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Options controls cluster bring-up.
type Options struct {
	// BinDir overrides the upstream-PG bin directory. Defaults to
	// `<repoRoot>/postgres/local_install/bin`.
	BinDir string
	// DataDir is the on-disk cluster directory. Optional — a fresh
	// tempdir is allocated when empty.
	DataDir string
	// LogPath captures postmaster stdout/stderr. Optional — a sibling
	// of DataDir is allocated when empty.
	LogPath string
	// Port is the listener port. Optional — an ephemeral 127.0.0.1
	// port is picked when zero.
	Port int
	// User is the bootstrap superuser. Defaults to $USER or "postgres".
	User string
	// Database is the default database for psql/Query. Defaults to "postgres".
	Database string
	// WalLevel is forwarded into `postgresql.conf`. Defaults to "logical".
	WalLevel string
	// MaxReplicationSlots / MaxWalSenders / MaxLogicalReplicationWorkers
	// — defaults to 4 each. Override for tests with denser fan-out.
	MaxReplicationSlots          int
	MaxWalSenders                int
	MaxLogicalReplicationWorkers int
	// ApplicationName is published as `application_name` in the
	// startup packet. Optional.
	ApplicationName string
	// ExtraConf is appended verbatim to postgresql.conf, one entry per
	// line. Use it for synchronous_standby_names / synchronous_commit
	// / etc.
	ExtraConf []string
	// StartupWait bounds the pg_ctl start invocation. Defaults to 30s.
	StartupWait time.Duration
	// RepoRoot resolves the default BinDir. Required when BinDir is
	// empty.
	RepoRoot string
}

// Cluster is a running upstream-PG instance.
type Cluster struct {
	name     string
	bin      string
	dataDir  string
	logPath  string
	port     int
	user     string
	database string
	libDir   string
	started  bool
}

// Available skips the calling test when the upstream PG bin directory
// is missing the binaries pgcluster needs.
func Available(t *testing.T, binDir string) {
	t.Helper()
	for _, tool := range []string{"initdb", "pg_ctl", "psql"} {
		if _, err := os.Stat(filepath.Join(binDir, tool)); err != nil {
			t.Skipf("upstream PG tool %q not found at %s: %v", tool, binDir, err)
			return
		}
	}
}

// New constructs a cluster but does not bring it up. Caller invokes
// Init then Start; Close tears down.
func New(name string, opts Options) (*Cluster, error) {
	c, err := newClusterHandle(name, opts)
	if err != nil {
		return nil, err
	}
	if err := c.runInitdb(); err != nil {
		return nil, err
	}
	if err := c.appendConf(opts); err != nil {
		return nil, err
	}
	return c, nil
}

// OpenExisting constructs a cluster handle rooted at an already-existing
// upstream-PG data directory (for example one produced by pg_basebackup).
// Unlike New, it does not run initdb or append any config.
func OpenExisting(name string, opts Options) (*Cluster, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("pgcluster: DataDir is required")
	}
	return newClusterHandle(name, opts)
}

func newClusterHandle(name string, opts Options) (*Cluster, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("pgcluster: name is required")
	}
	bin := opts.BinDir
	if strings.TrimSpace(bin) == "" {
		if strings.TrimSpace(opts.RepoRoot) == "" {
			return nil, errors.New("pgcluster: BinDir or RepoRoot is required")
		}
		bin = filepath.Join(opts.RepoRoot, "postgres", "local_install", "bin")
	}
	dataDir := opts.DataDir
	if strings.TrimSpace(dataDir) == "" {
		td, err := os.MkdirTemp("", "pgcluster-"+name+"-")
		if err != nil {
			return nil, fmt.Errorf("pgcluster: tempdir: %w", err)
		}
		dataDir = filepath.Join(td, "pgdata")
	}
	logPath := opts.LogPath
	if strings.TrimSpace(logPath) == "" {
		logPath = filepath.Join(filepath.Dir(dataDir), "pg.log")
	}
	port := opts.Port
	if port == 0 {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		port = p
	}
	user := strings.TrimSpace(opts.User)
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "postgres"
	}
	db := strings.TrimSpace(opts.Database)
	if db == "" {
		db = "postgres"
	}
	c := &Cluster{
		name:     name,
		bin:      bin,
		dataDir:  dataDir,
		logPath:  logPath,
		port:     port,
		user:     user,
		database: db,
		libDir:   filepath.Join(filepath.Dir(bin), "lib"),
	}
	return c, nil
}

func (c *Cluster) runInitdb() error {
	cmd := exec.Command(filepath.Join(c.bin, "initdb"),
		"-D", c.dataDir,
		"-U", c.user,
		"--auth-local=trust", "--auth-host=trust",
		"--no-sync",
	)
	cmd.Env = c.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pgcluster: initdb: %v\n%s", err, out)
	}
	return nil
}

func (c *Cluster) appendConf(opts Options) error {
	wl := strings.TrimSpace(opts.WalLevel)
	if wl == "" {
		wl = "logical"
	}
	mrs := opts.MaxReplicationSlots
	if mrs <= 0 {
		mrs = 4
	}
	mws := opts.MaxWalSenders
	if mws <= 0 {
		mws = 4
	}
	mlrw := opts.MaxLogicalReplicationWorkers
	if mlrw <= 0 {
		mlrw = 4
	}
	pgConf := filepath.Join(c.dataDir, "postgresql.conf")
	f, err := os.OpenFile(pgConf, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("pgcluster: open postgresql.conf: %w", err)
	}
	defer f.Close()
	fmt.Fprintf(f, "\nlisten_addresses = '127.0.0.1'\nport = %d\nfsync = off\n", c.port)
	fmt.Fprintf(f, "wal_level = %s\n", wl)
	fmt.Fprintf(f, "max_replication_slots = %d\n", mrs)
	fmt.Fprintf(f, "max_wal_senders = %d\n", mws)
	fmt.Fprintf(f, "max_logical_replication_workers = %d\n", mlrw)
	for _, line := range opts.ExtraConf {
		fmt.Fprintln(f, line)
	}
	return nil
}

// Start launches the cluster via `pg_ctl -w start`. Returns once the
// listener is accepting connections (pg_ctl `-w` waits internally).
func (c *Cluster) Start() error {
	if c.started {
		return errors.New("pgcluster: already started")
	}
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"),
		"-D", c.dataDir, "-l", c.logPath, "-w", "-t", "120",
		"-o", fmt.Sprintf("-p %d -h 127.0.0.1", c.port),
		"start")
	cmd.Env = c.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		logContent, _ := os.ReadFile(c.logPath)
		return fmt.Errorf("pgcluster: pg_ctl start: %v\n%s\n--- PG log ---\n%s", err, out, string(logContent))
	}
	c.started = true
	return nil
}

// Stop performs a fast shutdown. Errors are non-fatal — the cluster
// may have already crashed in a SIGKILL test.
func (c *Cluster) Stop() error {
	if !c.started {
		return nil
	}
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"),
		"-D", c.dataDir, "-m", "fast", "-w", "stop")
	cmd.Env = c.env()
	_ = cmd.Run()
	c.started = false
	return nil
}

// Kill issues `pg_ctl -m immediate stop`, the upstream equivalent of
// a SIGKILL of the postmaster process group.
func (c *Cluster) Kill() error {
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"),
		"-D", c.dataDir, "-m", "immediate", "-w", "stop")
	cmd.Env = c.env()
	out, err := cmd.CombinedOutput()
	c.started = false
	if err != nil {
		return fmt.Errorf("pgcluster: pg_ctl immediate stop: %v\n%s", err, out)
	}
	return nil
}

// Promote promotes a running standby via `pg_ctl promote -w`.
func (c *Cluster) Promote() error {
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"),
		"-D", c.dataDir, "-w", "promote")
	cmd.Env = c.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pgcluster: pg_ctl promote: %v\n%s", err, out)
	}
	return nil
}

// DataDir returns the on-disk path of the cluster.
func (c *Cluster) DataDir() string { return c.dataDir }

// Host always returns the loopback bind.
func (c *Cluster) Host() string { return "127.0.0.1" }

// Port returns the listener port.
func (c *Cluster) Port() int { return c.port }

// User returns the bootstrap superuser.
func (c *Cluster) User() string { return c.user }

// Database returns the default database name.
func (c *Cluster) Database() string { return c.database }

// Conninfo returns a libpq-style conninfo string suitable for
// embedding in `CREATE SUBSCRIPTION CONNECTION '…'` or
// `primary_conninfo`. The application_name suffix is appended when non-empty.
func (c *Cluster) Conninfo(applicationName string) string {
	base := fmt.Sprintf("host=%s port=%d user=%s dbname=%s",
		c.Host(), c.Port(), c.User(), c.Database())
	if strings.TrimSpace(applicationName) != "" {
		base += " application_name=" + applicationName
	}
	return base
}

// Exec runs psql with `-c sql` against the cluster. Fails the test
// if psql exits non-zero.
func (c *Cluster) Exec(t *testing.T, sqlText string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(c.bin, "psql"),
		"-h", c.Host(), "-p", fmt.Sprintf("%d", c.Port()),
		"-U", c.user, "-d", c.database,
		"-v", "ON_ERROR_STOP=1", "-c", sqlText)
	cmd.Env = c.env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pgcluster: psql exec %q: %v\n%s", sqlText, err, out)
	}
}

// QueryScalar runs `psql -tA -c sql` and returns the trimmed stdout.
// Use for one-cell SELECTs.
func (c *Cluster) QueryScalar(t *testing.T, sqlText string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(c.bin, "psql"),
		"-h", c.Host(), "-p", fmt.Sprintf("%d", c.Port()),
		"-U", c.user, "-d", c.database,
		"-tA", "-c", sqlText)
	cmd.Env = c.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pgcluster: psql query %q: %v\n%s", sqlText, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Pgbench runs the upstream pgbench binary against this cluster and
// returns combined stdout+stderr on success. Args are appended after
// the standard connection flags (-h/-p/-U <database>). Fails the test
// if pgbench exits non-zero. M0103-0007 rung 20 uses this helper to
// drive a pgbench-shape workload against a PG publisher with a goopg
// subscriber; the standard `-h/-p/-U <database>` shape matches the
// goopg analogue at internal/testutil/cluster/cluster.go::PGbench so
// the two binaries are interchangeable in PubSubCluster tests.
func (c *Cluster) Pgbench(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{
		"-h", c.Host(), "-p", fmt.Sprintf("%d", c.Port()),
		"-U", c.user, c.database,
	}, args...)
	cmd := exec.Command(filepath.Join(c.bin, "pgbench"), full...)
	cmd.Env = c.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pgcluster: pgbench %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// OpenDB returns a `database/sql` handle. Caller closes.
func (c *Cluster) OpenDB() (*sql.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable",
		c.Host(), c.Port(), c.User(), c.Database())
	return sql.Open("postgres", dsn)
}

// WaitReady polls `SELECT 1` until success or the deadline. Useful
// after a Start when the test wants a guarantee rather than the
// pg_ctl -w heuristic.
func (c *Cluster) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		db, err := c.OpenDB()
		if err == nil {
			var n int
			err = db.QueryRowContext(ctx, "SELECT 1").Scan(&n)
			_ = db.Close()
			if err == nil && n == 1 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("pgcluster: not ready within %s", timeout)
}

func (c *Cluster) env() []string {
	env := append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=")
	if c.libDir != "" {
		env = append(env, "LD_LIBRARY_PATH="+c.libDir)
	}
	return env
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
