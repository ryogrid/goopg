package tapnode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Host is Cluster.pm's host: the address psql reaches the node at.
func (n *Node) Host() string { h, _, _ := n.connParams(); return h }

// Port is Cluster.pm's port.
func (n *Node) Port() string { _, p, _ := n.connParams(); return p }

// User is the role the harness connects as (the bootstrap superuser).
func (n *Node) User() string { _, _, u := n.connParams(); return u }

// PSQLConnstr runs psql with --dbname connstr — Cluster.pm's psql with
// extra_params => ['--dbname' => $connstr] — feeding sql on stdin, unaligned
// and tuples-only. With onErrorStop, ON_ERROR_STOP=1 makes an SQL error exit
// 3 (on_error_die). It returns stdout without the trailing newline, stderr
// and the exit code. M0149-0077.
func (n *Node) PSQLConnstr(connstr, sql string, onErrorStop bool) (stdout, stderr string, exit int) {
	n.t.Helper()
	args := []string{"-X", "-A", "-t", "-q"}
	if onErrorStop {
		args = append(args, "-v", "ON_ERROR_STOP=1")
	}
	args = append(args, "--dbname", connstr, "-f", "-")
	return n.runPSQL(args, sql)
}

// PSQLCommands runs psql --dbname connstr with one --command per entry, the
// way 001_stream_rep.pl's command_fails_like and IPC::Run starts do.
func (n *Node) PSQLCommands(connstr string, commands ...string) (stdout, stderr string, exit int) {
	n.t.Helper()
	args := []string{"--no-psqlrc"}
	for _, c := range commands {
		args = append(args, "--command", c)
	}
	args = append(args, "--dbname", connstr)
	return n.runPSQL(args, "")
}

// PSQLCommandsCmd is PSQLCommands as an unstarted command, for a caller that
// runs it in the background (IPC::Run::start).
func (n *Node) PSQLCommandsCmd(connstr string, commands ...string) *exec.Cmd {
	args := []string{"--no-psqlrc"}
	for _, c := range commands {
		args = append(args, "--command", c)
	}
	args = append(args, "--dbname", connstr)
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), args...)
	cmd.Env = n.psqlEnv()
	return cmd
}

// psqlEnv is the client environment: Cluster.pm sets PGDATABASE=postgres
// for every test, and its bootstrap superuser is the default libpq user, so
// a connection string without dbname or user reaches the same place.
func (n *Node) psqlEnv() []string {
	return append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"PGDATABASE=postgres", "PGUSER="+n.User(),
		"LD_LIBRARY_PATH="+filepath.Join(n.repo, "postgres", "local_install", "lib"))
}

func (n *Node) runPSQL(args []string, stdin string) (string, string, int) {
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), args...)
	cmd.Env = n.psqlEnv()
	cmd.Stdin = strings.NewReader(stdin)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	exit := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			n.t.Fatalf("tapnode %s: psql: %v", n.name, err)
		}
	}
	return strings.TrimRight(o.String(), "\n"), e.String(), exit
}

// AdvanceWAL is Cluster.pm's advance_wal(num): num times, emit a
// non-transactional logical message and switch to a new WAL segment.
func (n *Node) AdvanceWAL(num int) {
	n.t.Helper()
	for i := 0; i < num; i++ {
		n.SafePSQL("postgres", "SELECT pg_logical_emit_message(false, '', 'foo');\nSELECT pg_switch_wal();")
	}
}
