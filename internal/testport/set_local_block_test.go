package testport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_SetLocalOutsideTransactionBlock pins SET LOCAL and the GUC side of
// autocommit transactions through psql on ONE session, against PG 18.3's
// output for the same script:
//   - a lone SET LOCAL warns 25P01 (WarnNoTransactionBlock) and changes
//     nothing, for GUCs and for SET LOCAL ROLE alike;
//   - inside a multi-statement message (an implicit block) it applies, with
//     no warning, until the message ends;
//   - set_config(..., true) lasts for its statement's transaction only;
//   - an aborted message undoes its plain SET (AtEOXact_GUC);
//   - a BEGIN later in the same message carries the local value into the
//     explicit block, which keeps it until COMMIT.
//
// Before, goopg never dropped the local layer outside an explicit block, so
// every one of these values leaked into the rest of the session.
func TestPort_SetLocalOutsideTransactionBlock(t *testing.T) {
	if psqlPath(t) == "" {
		t.Skip("psql not installed")
	}
	c := psqlCluster(t, "set_local_block")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	base := queryScalar(t, c, "SHOW work_mem")
	if base == "6MB" || base == "11MB" {
		t.Fatalf("fixture: unexpected base work_mem %q", base)
	}
	script := strings.Join([]string{
		`CREATE ROLE set_local_r1;`,
		`SET LOCAL work_mem = '5MB';`,
		`SHOW work_mem;`,
		`SET LOCAL work_mem = '6MB' \; SHOW work_mem;`,
		`SHOW work_mem;`,
		`SET work_mem = '7MB' \; SELECT 1/0;`,
		`SHOW work_mem;`,
		`SELECT set_config('work_mem', '12MB', true) || '/' || current_setting('work_mem');`,
		`SHOW work_mem;`,
		`SET LOCAL work_mem = '11MB' \; BEGIN \; SHOW work_mem;`,
		`SHOW work_mem;`,
		`COMMIT;`,
		`SHOW work_mem;`,
		`SET LOCAL ROLE set_local_r1;`,
		`SELECT current_user;`,
		`DROP ROLE set_local_r1;`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "set_local.sql")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	res := runPSQL(t, c, "-X", "-At", "-f", path)

	want := []string{
		base,        // lone SET LOCAL changed nothing
		"6MB",       // implicit block: applies
		base,        // ... until the message ends
		base,        // aborted message undid its plain SET
		"12MB/12MB", // set_config(..., true) within its statement
		base,        // ... and gone after it
		"11MB",      // promoted into the explicit block
		"11MB",      // still inside the block
		base,        // COMMIT ended it
		"postgres",  // lone SET LOCAL ROLE changed nothing
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		switch l {
		case "CREATE ROLE", "DROP ROLE", "SET", "BEGIN", "COMMIT", "":
			continue
		}
		got = append(got, l)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("SHOW sequence:\n got %v\nwant %v\nstderr:\n%s", got, want, res.Stderr)
	}
	warn := "WARNING:  SET LOCAL can only be used in transaction blocks"
	if n := strings.Count(res.Stderr, warn); n != 2 {
		t.Errorf("got %d %q warnings, want 2 (the two lone SET LOCALs)\nstderr:\n%s", n, warn, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "division by zero") {
		t.Errorf("missing the aborting error\nstderr:\n%s", res.Stderr)
	}
}
