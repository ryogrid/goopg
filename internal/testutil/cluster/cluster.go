package cluster

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/util"
)

// ShutdownMode mirrors pg_ctl's smart/fast/immediate vocabulary.
type ShutdownMode string

const (
	ShutdownSmart     ShutdownMode = "smart"
	ShutdownFast      ShutdownMode = "fast"
	ShutdownImmediate ShutdownMode = "immediate"
)

// Options controls cluster creation.
type Options struct {
	RepoRoot     string
	DataDir      string
	ListenAddr   string
	GoopgCommand []string // default: ["go", "run", "./cmd/goopg"]
	PSQLPath     string   // default: "psql"
	User         string   // default: "postgres"
	Database     string   // default: "postgres"
	StartupWait  time.Duration
	ShutdownWait time.Duration
}

// Cluster is a single goopg test instance (multi-cluster orchestration is
// intentionally deferred in M0004).
type Cluster struct {
	name         string
	repoRoot     string
	dataDir      string
	listenAddr   string
	goopgCommand []string
	psqlPath     string
	user         string
	database     string
	startupWait  time.Duration
	shutdownWait time.Duration
	logPath      string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// BackgroundPSQL is a long-lived psql session started by StartPSQL.
type BackgroundPSQL struct {
	cmd *exec.Cmd
}

// New builds a cluster handle.
func New(name string, opts Options) (*Cluster, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("cluster name is required")
	}
	if strings.TrimSpace(opts.RepoRoot) == "" {
		return nil, errors.New("RepoRoot is required")
	}
	listen := opts.ListenAddr
	if strings.TrimSpace(listen) == "" {
		p, err := freePort()
		if err != nil {
			return nil, err
		}
		listen = fmt.Sprintf("127.0.0.1:%d", p)
	}
	dataDir := opts.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = filepath.Join(opts.RepoRoot, "tmp", "clusters", name)
	}
	goopg := opts.GoopgCommand
	if len(goopg) == 0 {
		goopg = []string{"go", "run", "./cmd/goopg"}
	}
	psql := opts.PSQLPath
	if strings.TrimSpace(psql) == "" {
		psql = "psql"
	}
	user := opts.User
	if strings.TrimSpace(user) == "" {
		user = "postgres"
	}
	db := opts.Database
	if strings.TrimSpace(db) == "" {
		db = "postgres"
	}
	startup := opts.StartupWait
	if startup <= 0 {
		startup = 10 * time.Second
	}
	shutdown := opts.ShutdownWait
	if shutdown <= 0 {
		shutdown = 10 * time.Second
	}
	return &Cluster{
		name:         name,
		repoRoot:     opts.RepoRoot,
		dataDir:      dataDir,
		listenAddr:   listen,
		goopgCommand: goopg,
		psqlPath:     psql,
		user:         user,
		database:     db,
		startupWait:  startup,
		shutdownWait: shutdown,
		logPath:      filepath.Join(dataDir, "cluster.log"),
	}, nil
}

func (c *Cluster) Name() string       { return c.name }
func (c *Cluster) DataDir() string    { return c.dataDir }
func (c *Cluster) ListenAddr() string { return c.listenAddr }
func (c *Cluster) LogPath() string    { return c.logPath }

// Init runs `goopg init -D <dir>`.
func (c *Cluster) Init() error {
	res, err := c.runGoopg("init", "-D", c.dataDir)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("init failed: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Start runs `goopg start` in the background and waits until status reports
// "running".
func (c *Cluster) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil && c.cmd.Process != nil {
		return errors.New("cluster already started")
	}
	if err := os.MkdirAll(c.dataDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.logPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(c.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	args := append(append([]string{}, c.goopgCommand[1:]...), "start", "-D", c.dataDir, "-listen", c.listenAddr)
	cmd := exec.Command(c.goopgCommand[0], args...)
	cmd.Dir = c.repoRoot
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		_ = f.Close()
		return err
	}
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
	}()
	c.cmd = cmd

	deadline := time.Now().Add(c.startupWait)
	for time.Now().Before(deadline) {
		status, _, err := c.Status()
		if err == nil && status == 0 {
			return nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("start failed; process exited early (see %s)", c.logPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("start timeout after %s (see %s)", c.startupWait, c.logPath)
}

// Stop runs `goopg stop -D ...` with the requested shutdown mode.
func (c *Cluster) Stop(mode ShutdownMode) error {
	if mode == "" {
		mode = ShutdownFast
	}
	res, err := c.runGoopg("stop", "-D", c.dataDir, "-mode", string(mode), "-t", strconv.Itoa(int(c.shutdownWait.Seconds())))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("stop failed: %s", strings.TrimSpace(res.Stderr))
	}
	c.mu.Lock()
	c.cmd = nil
	c.mu.Unlock()
	return nil
}

// Restart performs stop+start.
func (c *Cluster) Restart(mode ShutdownMode) error {
	if err := c.Stop(mode); err != nil {
		return err
	}
	return c.Start()
}

// Reload runs `goopg reload -D <dir>`.
func (c *Cluster) Reload() error {
	res, err := c.runGoopg("reload", "-D", c.dataDir)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("reload failed: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Status runs `goopg status -D <dir>` and returns (exitCode, combinedOutput).
func (c *Cluster) Status() (int, string, error) {
	res, err := c.runGoopg("status", "-D", c.dataDir)
	if err != nil {
		return 1, "", err
	}
	msg := strings.TrimSpace(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
	return res.ExitCode, msg, nil
}

// PSQL runs a foreground psql command against this cluster.
func (c *Cluster) PSQL(args ...string) (util.CommandResult, error) {
	host, port, err := splitHostPort(c.listenAddr)
	if err != nil {
		return util.CommandResult{}, err
	}
	base := []string{"-h", host, "-p", port, "-U", c.user, "-d", c.database}
	base = append(base, args...)
	return util.RunCommand(util.CommandSpec{
		Name:    c.psqlPath,
		Args:    base,
		Dir:     c.repoRoot,
		Env:     []string{"PGPASSWORD="},
		Timeout: 30 * time.Second,
	})
}

// StartPSQL starts a background psql process for long-running scripts.
func (c *Cluster) StartPSQL(args []string, stdin string) (*BackgroundPSQL, error) {
	host, port, err := splitHostPort(c.listenAddr)
	if err != nil {
		return nil, err
	}
	base := []string{"-h", host, "-p", port, "-U", c.user, "-d", c.database}
	base = append(base, args...)
	cmd := exec.Command(c.psqlPath, base...)
	cmd.Dir = c.repoRoot
	cmd.Env = append(os.Environ(), "PGPASSWORD=")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &BackgroundPSQL{cmd: cmd}, nil
}

func (s *BackgroundPSQL) Wait() error {
	if s == nil || s.cmd == nil {
		return nil
	}
	return s.cmd.Wait()
}

func (s *BackgroundPSQL) Stop() error {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// Query executes SQL using Go's lib/pq database/sql client and returns rows as
// text values.
func (c *Cluster) Query(ctx context.Context, sqlText string) ([][]string, error) {
	host, port, err := splitHostPort(c.listenAddr)
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=disable", host, port, c.user, c.database)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AppendPostgresqlConf appends one line to postgresql.conf.
func (c *Cluster) AppendPostgresqlConf(line string) error {
	return appendLine(filepath.Join(c.dataDir, "postgresql.conf"), line)
}

// AppendPGHBA appends one line to pg_hba.conf.
func (c *Cluster) AppendPGHBA(line string) error {
	return appendLine(filepath.Join(c.dataDir, "pg_hba.conf"), line)
}

// WaitForLogContains waits until cluster log output contains needle.
func (c *Cluster) WaitForLogContains(needle string, timeout time.Duration) error {
	return util.WaitForFileContains(c.logPath, needle, timeout)
}

func (c *Cluster) runGoopg(args ...string) (util.CommandResult, error) {
	cmdArgs := append(append([]string{}, c.goopgCommand[1:]...), args...)
	return util.RunCommand(util.CommandSpec{
		Name:    c.goopgCommand[0],
		Args:    cmdArgs,
		Dir:     c.repoRoot,
		Timeout: 30 * time.Second,
	})
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0, err
	}
	return port, nil
}

func splitHostPort(addr string) (string, string, error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	return h, p, nil
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err = f.WriteString(line)
	return err
}
