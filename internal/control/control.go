// Package control implements the operator-facing control plane for
// the goopg server: a Unix-domain command socket plus a
// `postmaster.pid`-style file under the data directory. Together
// they replace the signal-driven `pg_ctl stop|reload|status`
// machinery upstream PostgreSQL relies on, per
// .ralph/specs/GOAL_AND_REQUIREMENTS.md §3.3.
//
// File layout:
//
//	<DataDir>/postmaster.pid       # text file, one value per line
//	<DataDir>/.goopg.ctl.sock      # Unix-domain command socket
//
// The pidfile carries everything `goopg status` needs without
// touching the socket: process pid, listen address, socket path,
// and a monotonic startup nonce. The socket carries everything that
// requires the running process to act (graceful shutdown, config
// reload).
package control

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PIDFileName is the filename goopg writes its pid metadata to,
// matching upstream's `postmaster.pid` so an operator familiar with
// PostgreSQL doesn't have to learn a new layout. The contents are
// goopg-specific (different field set), but the path is the same.
const PIDFileName = "postmaster.pid"

// SocketName is the relative path of the control socket inside the
// data directory. It starts with `.` so a casual `ls` of an
// upstream-shaped data directory doesn't show goopg-only files in
// the natural sort order, and is short enough to fit within the
// sun_path 108-byte limit on Linux when the data directory is at a
// reasonable depth.
const SocketName = ".goopg.ctl.sock"

// PIDFile is the parsed contents of <DataDir>/postmaster.pid.
type PIDFile struct {
	PID        int
	DataDir    string
	StartedAt  time.Time
	ListenAddr string
	SocketPath string
}

// Marshal renders the pidfile in goopg's text-line format. v0 keeps
// it simple on purpose; future loops can extend it without needing
// a new file because every line after the first four is treated as
// optional by Parse.
func (p PIDFile) Marshal() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", p.PID)
	fmt.Fprintf(&b, "%s\n", p.DataDir)
	fmt.Fprintf(&b, "%d\n", p.StartedAt.UnixMilli())
	fmt.Fprintf(&b, "%s\n", p.ListenAddr)
	fmt.Fprintf(&b, "%s\n", p.SocketPath)
	return []byte(b.String())
}

// ParsePIDFile reads <dir>/postmaster.pid into a PIDFile. Missing
// file returns os.ErrNotExist so callers can distinguish "no
// running server" from "corrupt pidfile".
func ParsePIDFile(dir string) (PIDFile, error) {
	path := filepath.Join(dir, PIDFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return PIDFile{}, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 5 {
		return PIDFile{}, fmt.Errorf("%s: expected at least 5 lines, got %d", path, len(lines))
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return PIDFile{}, fmt.Errorf("%s: bad pid line %q: %w", path, lines[0], err)
	}
	startedMs, err := strconv.ParseInt(lines[2], 10, 64)
	if err != nil {
		return PIDFile{}, fmt.Errorf("%s: bad start-time line %q: %w", path, lines[2], err)
	}
	return PIDFile{
		PID:        pid,
		DataDir:    lines[1],
		StartedAt:  time.UnixMilli(startedMs),
		ListenAddr: lines[3],
		SocketPath: lines[4],
	}, nil
}

// WritePIDFile atomically writes a pidfile under dir, replacing any
// existing one. Atomicity matters because `goopg status` may race
// with startup.
func WritePIDFile(dir string, p PIDFile) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("control: mkdir %q: %w", dir, err)
	}
	path := filepath.Join(dir, PIDFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, p.Marshal(), 0o600); err != nil {
		return fmt.Errorf("control: write pidfile tempfile: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("control: rename pidfile: %w", err)
	}
	return nil
}

// RemovePIDFile clears the pidfile. Missing file is not an error —
// the operator may have already deleted it manually.
func RemovePIDFile(dir string) error {
	err := os.Remove(filepath.Join(dir, PIDFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ProcessAlive reports whether the process named in pid is still
// running. On Linux, kill(pid, 0) returns nil for live processes
// and ESRCH for missing ones.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but is owned by another user;
	// still alive as far as goopg status cares.
	return errors.Is(err, syscall.EPERM)
}

// Listener accepts control-plane commands on a Unix domain socket.
// One Listener owns one socket file; Close removes the file and
// stops accepting new connections.
type Listener struct {
	path string
	ln   net.Listener
	stop chan struct{}
	wg   sync.WaitGroup

	// OnStop is invoked when a client sends STOP. The listener does
	// not close itself — the caller is expected to trigger their
	// shutdown sequence (typically by cancelling the server
	// context), which then drains the listener.
	OnStop func() error
	// OnReload is invoked when a client sends RELOAD. Should be
	// fast; v0's only consumer is a no-op.
	OnReload func() error
}

// NewListener binds a control-plane Unix domain socket at path.
// Existing socket files are unlinked first; this is safe because a
// stale socket from a crashed previous run is the common case.
func NewListener(path string) (*Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("control: mkdir for socket: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("control: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("control: listen %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("control: chmod socket: %w", err)
	}
	return &Listener{
		path: path,
		ln:   ln,
		stop: make(chan struct{}),
	}, nil
}

// Path returns the socket path.
func (l *Listener) Path() string { return l.path }

// Serve accepts connections in a loop until Close is called.
// Returns nil on a clean Close, or the underlying accept error.
func (l *Listener) Serve() error {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			select {
			case <-l.stop:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handleConn(conn)
		}()
	}
}

// Close stops the listener and removes the socket file. Pending
// handlers drain.
func (l *Listener) Close() error {
	close(l.stop)
	err := l.ln.Close()
	l.wg.Wait()
	_ = os.Remove(l.path)
	return err
}

func (l *Listener) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	cmd := strings.TrimSpace(line)
	switch cmd {
	case "PING":
		_, _ = conn.Write([]byte("OK\n"))
	case "STOP":
		_, _ = conn.Write([]byte("OK\n"))
		if l.OnStop != nil {
			_ = l.OnStop()
		}
	case "RELOAD":
		if l.OnReload != nil {
			if err := l.OnReload(); err != nil {
				fmt.Fprintf(conn, "ERR %s\n", err.Error())
				return
			}
		}
		_, _ = conn.Write([]byte("OK\n"))
	default:
		fmt.Fprintf(conn, "ERR unknown command %q\n", cmd)
	}
}

// Send connects to the control socket at path, sends one command
// line, and reads the single-line reply. Returns the reply with a
// trailing newline stripped.
func Send(path, cmd string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	conn, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		return "", err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\n"), nil
}
