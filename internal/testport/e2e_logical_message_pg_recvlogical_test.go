package testport

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_LogicalMessageReachesPgRecvlogical drives goopg's logical
// walsender with PG 18.3's own client, pg_recvlogical, using pgoutput with
// messages = true (the logical-decoding message oracle):
//
//   - a transactional pg_logical_emit_message arrives as MESSAGE with
//     flags 1 inside its transaction's BEGIN / COMMIT;
//   - a non-transactional one arrives as MESSAGE with flags 0 on its own;
//   - a transactional one in a rolled-back transaction never arrives.
//
// It also needs IDENTIFY_SYSTEM to name the database on a
// replication=database connection: without it pg_recvlogical stops with
// "could not establish database-specific replication connection".
func TestE2E_LogicalMessageReachesPgRecvlogical(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping replication test in short mode")
	}
	repo := repoRoot(t)
	recvlogical := clientToolBin(t, "pg_recvlogical")
	if recvlogical == "" {
		t.Skip("pg_recvlogical not found")
	}
	baseDir := t.TempDir()
	c, err := cluster.New("lm-recvlogical", cluster.Options{
		RepoRoot:     repo,
		DataDir:      filepath.Join(baseDir, "goopg"),
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	conf, err := os.OpenFile(filepath.Join(c.DataDir(), "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conf.WriteString("\nwal_level = logical\n")
	_ = conf.Close()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, q := range []string{"CREATE TABLE lm_t (a int PRIMARY KEY)", "CREATE PUBLICATION lm_p FOR TABLE lm_t"} {
		if err := runSQLSimple(t, c, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	port := mustGoopgPort(c.ListenAddr())
	base := []string{"-h", "127.0.0.1", "-p", port, "-U", "postgres", "-d", "postgres", "-S", "lm_s"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	create := exec.CommandContext(ctx, recvlogical, append(base, "--create-slot", "-P", "pgoutput")...)
	create.Env = clientToolEnv(repo)
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("pg_recvlogical --create-slot: %v\n%s", err, out)
	}
	outFile := filepath.Join(baseDir, "stream.bin")
	var stderr bytes.Buffer
	stream := exec.CommandContext(ctx, recvlogical, append(base, "--start", "-o", "proto_version=1",
		"-o", "publication_names=lm_p", "-o", "messages=true", "-F", "1", "-f", outFile)...)
	stream.Env = clientToolEnv(repo)
	stream.Stderr = &stderr
	if err := stream.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Process.Kill(); _ = stream.Wait() }()
	time.Sleep(2 * time.Second)

	for _, q := range []string{
		"SELECT pg_logical_emit_message(true, 'gooprefix', 'in-xact')",
		"SELECT pg_logical_emit_message(false, 'gooprefix', 'no-xact')",
	} {
		if err := runSQLSimple(t, c, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := runSQLSimple(t, c, "BEGIN; INSERT INTO lm_t VALUES (1); SELECT pg_logical_emit_message(true, 'gooprefix', 'rolled-back'); ROLLBACK"); err != nil {
		t.Fatalf("rolled-back transaction: %v", err)
	}
	if err := runSQLSimple(t, c, "INSERT INTO lm_t VALUES (2)"); err != nil {
		t.Fatal(err)
	}

	var data []byte
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(outFile)
		if bytes.Count(data, []byte("C\x00")) >= 2 && bytes.Contains(data, []byte("no-xact")) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// MESSAGE: 'M' flags(1) lsn(8) prefix\0 len(4) content.
	inXact := bytes.Index(data, []byte("gooprefix\x00\x00\x00\x00\x07in-xact"))
	noXact := bytes.Index(data, []byte("gooprefix\x00\x00\x00\x00\x07no-xact"))
	if inXact < 0 || noXact < 0 {
		t.Fatalf("pg_recvlogical output lacks the messages (stderr %q):\n% x", stderr.String(), data)
	}
	if hdr := inXact - 10; hdr < 0 || data[hdr] != 'M' || data[hdr+1] != 1 {
		t.Errorf("transactional message header % x, want 'M' flags 1", data[max(0, hdr):inXact])
	}
	if hdr := noXact - 10; hdr < 0 || data[hdr] != 'M' || data[hdr+1] != 0 {
		t.Errorf("non-transactional message header % x, want 'M' flags 0", data[max(0, hdr):noXact])
	}
	if bytes.Contains(data, []byte("rolled-back")) {
		t.Error("a message from a rolled-back transaction reached the subscriber")
	}
	// The transactional message sits inside a BEGIN ... COMMIT; the
	// non-transactional one does not.
	lastB := bytes.LastIndexByte(data[:inXact], 'B')
	if lastB < 0 || bytes.IndexByte(data[lastB:inXact], 'C') >= 0 {
		t.Errorf("transactional message is not inside a BEGIN/COMMIT:\n% x", data)
	}
}
