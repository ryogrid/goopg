package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGoopgEnv turns the test binary into a goopg stand-in whose start hangs
// inside startup, as the 2026-10-20 nightly's servers did inside initdb.Open.
const fakeGoopgEnv = "GOOPG_CLUSTER_FAKE_STUCK_START"

func TestMain(m *testing.M) {
	if os.Getenv(fakeGoopgEnv) != "" {
		os.Exit(fakeGoopg(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeGoopg(args []string) int {
	if len(args) > 0 && args[0] == "start" {
		fmt.Fprintln(os.Stderr, `level=INFO msg="loaded postgresql.conf"`)
		fakeStuckOpen()
	}
	// status: not running (pg_ctl's exit 3).
	return 3
}

//go:noinline
func fakeStuckOpen() {
	time.Sleep(time.Hour)
}

// TestStartTimeoutCarriesStuckGoroutines pins AI-20261020-013101-003/-004's
// diagnostic: a start that times out inside startup sends the server SIGQUIT
// and inlines goroutine 1's stack, so a nightly failure names the frame it
// was stuck in although its cluster.log is deleted with the run.
func TestStartTimeoutCarriesStuckGoroutines(t *testing.T) {
	t.Setenv(fakeGoopgEnv, "1")
	c, err := New("stuck", Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		GoopgCommand: []string{os.Args[0]},
		StartupWait:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Start()
	if err == nil {
		t.Fatal("Start succeeded against a server stuck in startup")
	}
	msg := err.Error()
	for _, want := range []string{
		"start timeout after 1s",
		`msg="loaded postgresql.conf"`,
		"--- stuck-start goroutines (SIGQUIT) ---",
		"goroutine 1 ",
		"cluster.fakeStuckOpen",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("start timeout lacks %q:\n%s", want, msg)
		}
	}
	// Under cgroup v2 the timeout also names the group's memory state, which
	// tells a throttled start (memory.high) from a stuck one.
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		if !strings.Contains(msg, " memory.high=") || !strings.Contains(msg, " high-events=") {
			t.Errorf("start timeout lacks the cgroup memory note:\n%s", msg)
		}
	}
}

func TestStartupGoroutinesSelectsMainAndInitdb(t *testing.T) {
	dump := `SIGQUIT: quit
PC=0x47a2c1 m=0 sigcode=0

goroutine 0 gp=0x1 m=0 mp=0x2 [idle]:
runtime.futex(...)

goroutine 1 [select]:
github.com/goopg/goopg/internal/initdb.Open({...})
main.runStart(...)

goroutine 17 gp=0xc0000a2000 m=nil [chan receive]:
net/http.(*Server).Serve(...)

goroutine 42 gp=0xc0000a3000 m=nil [semacquire]:
github.com/goopg/goopg/internal/initdb.Open.func71()
`
	got := startupGoroutines(dump)
	if !strings.Contains(got, "goroutine 1 [select]") || !strings.Contains(got, "goroutine 42 gp=") {
		t.Errorf("want goroutines 1 and 42, got:\n%s", got)
	}
	if strings.Contains(got, "goroutine 17 ") || strings.Contains(got, "goroutine 0 ") || strings.Contains(got, "SIGQUIT") {
		t.Errorf("unrelated goroutines kept:\n%s", got)
	}
}
