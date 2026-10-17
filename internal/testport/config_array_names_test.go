package testport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_ConfigArraysStoreCanonicalNames pins GUCArrayAdd / GUCArrayDelete
// end to end through the real binary, against PG 18.3's catalog contents
// for the same statements: pg_proc.proconfig and pg_db_role_setting.setconfig
// store each setting under the variable's own name (DateStyle, TimeZone,
// sort_mem -> work_mem), RESET finds it by any spelling, and a name or value
// PostgreSQL rejects (validate_option_array_item) fails the statement
// instead of being stored. Before, goopg stored the name as typed and
// accepted anything.
func TestPort_ConfigArraysStoreCanonicalNames(t *testing.T) {
	c, err := cluster.New("config-array-names", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, stmt := range []string{
		`CREATE FUNCTION cfg_g() RETURNS int LANGUAGE sql SET datestyle = iso, mdy SET TIMEZONE = 'UTC' SET Work_Mem = '1MB' AS 'select 1'`,
		`ALTER FUNCTION cfg_g() SET DATESTYLE = sql`,
		`ALTER FUNCTION cfg_g() RESET timezone`,
		`CREATE ROLE cfg_r1`,
		`ALTER ROLE cfg_r1 SET datestyle = iso`,
		`ALTER ROLE cfg_r1 SET TIMEZONE TO 'UTC'`,
		`ALTER ROLE cfg_r1 SET SORT_MEM = 2000`,
		`ALTER ROLE cfg_r1 SET "my.X" = 'y'`,
		`ALTER ROLE cfg_r1 RESET DateStyle`,
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if got := queryScalar(t, c, `SELECT proconfig FROM pg_proc WHERE proname = 'cfg_g'`); got != `{DateStyle=sql,work_mem=1MB}` {
		t.Errorf("proconfig = %s, want {DateStyle=sql,work_mem=1MB}", got)
	}
	if got := queryScalar(t, c, `SELECT setconfig FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole WHERE rolname = 'cfg_r1'`); got != `{TimeZone=UTC,work_mem=2000,my.X=y}` {
		t.Errorf("setconfig = %s, want {TimeZone=UTC,work_mem=2000,my.X=y}", got)
	}
	for stmt, want := range map[string]string{
		`ALTER ROLE cfg_r1 SET no_such_guc = 1`:                                                 `unrecognized configuration parameter "no_such_guc"`,
		`ALTER ROLE cfg_r1 SET shared_buffers = '1GB'`:                                          `parameter "shared_buffers" cannot be changed without restarting the server`,
		`ALTER ROLE cfg_r1 SET server_version = '1'`:                                            `parameter "server_version" cannot be changed`,
		`ALTER ROLE cfg_r1 SET work_mem = 'bogus'`:                                              `"bogus"`,
		`CREATE FUNCTION cfg_h() RETURNS int LANGUAGE sql SET work_mem = 'bogus' AS 'select 1'`: `"bogus"`,
		`ALTER DATABASE postgres SET no_such_guc = 1`:                                           `unrecognized configuration parameter "no_such_guc"`,
	} {
		err := runSQLSimple(t, c, stmt)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v, want %q", stmt, err, want)
		}
	}
}
