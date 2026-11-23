package tapnode

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PumpedPSQL is the IPC::Run-driven psql of TAP tests such as
// 021_row_visibility.pl: `psql --no-psqlrc --no-align --file -` with stdin
// fed query by query and stdout accumulated, so a test can wait for a pattern
// (send_query_and_wait). M0149-0012.
type PumpedPSQL struct {
	n     *Node
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   strings.Builder
	done  chan struct{}
}

// PumpedPSQL starts the session against db.
func (n *Node) PumpedPSQL(db string) *PumpedPSQL {
	n.t.Helper()
	host, port, user := n.connParams()
	cmd := exec.Command(filepath.Join(n.binDir, "psql"), "--no-psqlrc", "--no-align", "--file", "-",
		"--dbname", "host="+host+" port="+port+" user="+user+" dbname="+db)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(n.repo, "postgres", "local_install", "lib"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		n.t.Fatalf("tapnode: pumped psql stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		n.t.Fatalf("tapnode: pumped psql stdout: %v", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		n.t.Fatalf("tapnode: start pumped psql: %v", err)
	}
	p := &PumpedPSQL{n: n, cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		buf := make([]byte, 4096)
		for {
			k, err := stdout.Read(buf)
			if k > 0 {
				p.mu.Lock()
				p.out.Write(buf[:k])
				p.mu.Unlock()
			}
			if err != nil {
				close(p.done)
				return
			}
		}
	}()
	n.t.Cleanup(func() {
		_ = p.stdin.Close()
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	})
	return p
}

// SendQueryAndWait is the .pl's send_query_and_wait: it sends query, waits
// until the accumulated stdout matches until (multi-line), then clears the
// buffer. It reports false on timeout (180 s) or when psql exits.
func (p *PumpedPSQL) SendQueryAndWait(query, until string) bool {
	p.n.t.Helper()
	re := regexp.MustCompile("(?m)" + until)
	if _, err := io.WriteString(p.stdin, query+"\n"); err != nil {
		p.n.t.Logf("tapnode: pumped psql write: %v", err)
		return false
	}
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := p.out.String()
		if re.MatchString(got) {
			p.out.Reset()
			p.mu.Unlock()
			return true
		}
		p.mu.Unlock()
		select {
		case <-p.done:
			p.n.t.Logf("aborting wait: program died\nstream contents: >>%s<<\npattern searched for: %s", got, until)
			return false
		case <-time.After(20 * time.Millisecond):
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n.t.Logf("aborting wait: program timed out\nstream contents: >>%s<<\npattern searched for: %s", p.out.String(), until)
	return false
}

// Quit sends \q and waits for psql to exit.
func (p *PumpedPSQL) Quit() {
	_, _ = io.WriteString(p.stdin, "\\q\n")
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
	}
}
