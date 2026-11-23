package tapnode

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// BackgroundPSQL is BackgroundPsql.pm: one long-lived psql session whose
// stdin stays open, so a test can interleave queries across sessions.
// Query sends a statement followed by a banner and reads stdout up to the
// banner, as BackgroundPsql::query does.
type BackgroundPSQL struct {
	t      testing.TB
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    chan string // stdout lines
	mu     sync.Mutex
	stderr strings.Builder
	seq    int
}

// BackgroundPSQL starts `psql -XAtq -f -` against db. onErrorStop sets
// ON_ERROR_STOP, as background_psql's on_error_stop option does.
func (n *Node) BackgroundPSQL(db string, onErrorStop bool) *BackgroundPSQL {
	n.t.Helper()
	host, port, user := n.connParams()
	stop := "0"
	if onErrorStop {
		stop = "1"
	}
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), "-X", "-A", "-t", "-q",
		"-v", "ON_ERROR_STOP="+stop, "-h", host, "-p", port, "-U", user, "-d", db, "-f", "-")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(n.repo, "postgres", "local_install", "lib"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		n.t.Fatalf("tapnode: background psql stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		n.t.Fatalf("tapnode: background psql stdout: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		n.t.Fatalf("tapnode: background psql stderr: %v", err)
	}
	if err := cmd.Start(); err != nil {
		n.t.Fatalf("tapnode: start background psql: %v", err)
	}
	b := &BackgroundPSQL{t: n.t, cmd: cmd, stdin: stdin, out: make(chan string, 1024)}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			b.out <- sc.Text()
		}
		close(b.out)
	}()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			b.mu.Lock()
			b.stderr.WriteString(sc.Text() + "\n")
			b.mu.Unlock()
		}
	}()
	n.t.Cleanup(func() { _ = b.Quit() })
	return b
}

// connParams are the host, port and role psql connects with.
func (n *Node) connParams() (host, port, user string) {
	if n.g != nil {
		h, p, err := net.SplitHostPort(n.g.ListenAddr())
		if err != nil {
			n.t.Fatalf("tapnode: listen address %q: %v", n.g.ListenAddr(), err)
		}
		return h, p, n.g.User()
	}
	return n.p.Host(), fmt.Sprint(n.p.Port()), n.p.User()
}

// Query is BackgroundPsql::query: it runs sql and returns its stdout, with
// the trailing newline removed, once the banner after it arrives.
func (b *BackgroundPSQL) Query(sql string) string {
	b.t.Helper()
	b.seq++
	banner := fmt.Sprintf("background_psql: QUERY_SEPARATOR %d:", b.seq)
	if _, err := io.WriteString(b.stdin, sql+"\n;\n\\echo "+banner+"\n"); err != nil {
		b.t.Fatalf("tapnode: background psql write: %v", err)
	}
	var lines []string
	timeout := time.After(3 * time.Minute)
	for {
		select {
		case line, ok := <-b.out:
			if !ok {
				b.t.Fatalf("tapnode: background psql exited before %q; stderr:\n%s", sql, b.Stderr())
			}
			if line == banner {
				return strings.TrimRight(strings.Join(lines, "\n"), "\n")
			}
			lines = append(lines, line)
		case <-timeout:
			b.t.Fatalf("tapnode: background psql timed out on %q; stderr:\n%s", sql, b.Stderr())
		}
	}
}

// QuerySafe is BackgroundPsql::query_safe: Query that fails the test when
// the statement wrote anything to stderr.
func (b *BackgroundPSQL) QuerySafe(sql string) string {
	b.t.Helper()
	b.ClearStderr()
	out := b.Query(sql)
	if e := b.Stderr(); e != "" {
		b.t.Fatalf("tapnode: query_safe(%q) wrote to stderr:\n%s", sql, e)
	}
	return out
}

// Send is BackgroundPsql::query_until with an empty pattern: it writes sql
// and returns without waiting for it to finish.
func (b *BackgroundPSQL) Send(sql string) {
	b.t.Helper()
	if _, err := io.WriteString(b.stdin, sql); err != nil {
		b.t.Fatalf("tapnode: background psql write: %v", err)
	}
}

// WaitStderr is pump_until on the session's stderr: it reports whether re
// matched within timeout.
func (b *BackgroundPSQL) WaitStderr(re *regexp.Regexp, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if re.MatchString(b.Stderr()) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return re.MatchString(b.Stderr())
}

// Stderr returns what the session has written to stderr so far.
func (b *BackgroundPSQL) Stderr() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stderr.String()
}

// ClearStderr forgets the session's stderr so far.
func (b *BackgroundPSQL) ClearStderr() {
	b.mu.Lock()
	b.stderr.Reset()
	b.mu.Unlock()
}

// Quit is BackgroundPsql::quit: it closes stdin and waits for psql to exit.
func (b *BackgroundPSQL) Quit() error {
	if b.cmd == nil {
		return nil
	}
	_ = b.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		_ = b.cmd.Process.Kill()
		err = <-done
	}
	b.cmd = nil
	return err
}

// PSQLExit is Cluster.pm's psql(dbname, sql) returning psql's exit code with
// its stdout and stderr: 0 on success, 2 when the connection fails or is
// lost (a FATAL), 3 when a statement errors under ON_ERROR_STOP.
func (n *Node) PSQLExit(db, sqlText string) (stdout, stderr string, exit int) {
	n.t.Helper()
	host, port, user := n.connParams()
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), "-X", "-A", "-t", "-q",
		"-v", "ON_ERROR_STOP=1", "-h", host, "-p", port, "-U", user, "-d", db, "-f", "-")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(n.repo, "postgres", "local_install", "lib"))
	cmd.Stdin = strings.NewReader(sqlText)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	exit = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			n.t.Fatalf("tapnode: psql: %v", err)
		}
	}
	return strings.TrimRight(o.String(), "\n"), e.String(), exit
}
