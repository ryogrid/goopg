# testport/framework

Package `framework` provides the shared harness used by goopg's PostgreSQL
oracle test-port suite.  It contains three independent sub-systems:

- **Isolation tester** — multi-session concurrent executor for `.spec` files
- **Regress runner** — single-session SQL/expected comparison for pg_regress cases
- **Status/inventory** — CSV-backed tracking of port/defer/excluded status

---

## Isolation tester

### Overview

`IsolationRunner` executes PostgreSQL isolation test specs
(`postgres/src/test/isolation/specs/*.spec`) against a live goopg instance and
compares output to the reference files in
`postgres/src/test/isolation/expected/*.out`.

The implementation follows the upstream `isolationtester` model:

| Upstream isolationtester | This package |
|--------------------------|--------------|
| Per-session PGconn | `*sql.Conn` via `db.Conn(ctx)` (one per session) |
| Blocking detection via `pg_stat_activity` | 300 ms goroutine timeout |
| PQprint output (`align=true`, `fieldSep="|"`) | `pqprintFormat` function |
| `<waiting ...>` / `<... completed>` annotations | Goroutine drain loop |
| Global setup once / per-session setup per permutation | Same |

### Spec file format

```
# Global setup — runs once before all permutations.
setup
{
    CREATE TABLE t (id int PRIMARY KEY, v text);
}

teardown
{
    DROP TABLE t;
}

session s1
# Per-session setup — runs before each permutation on this session's connection.
setup { BEGIN; }
step read  { SELECT * FROM t WHERE id = 1; }
step write { INSERT INTO t VALUES (1, 'a'); }
step done  { COMMIT; }

session s2
setup { BEGIN; }
step read2 { SELECT * FROM t WHERE id = 1; }
step done2 { COMMIT; }

permutation read read2 write done done2
permutation write read read2 done done2
```

- `session name` — declares a named session; subsequent `setup`, `teardown`,
  and `step` blocks belong to this session until the next `session` declaration.
- `setup { SQL }` — at global scope: runs once before all permutations on a
  dedicated monitor connection.  At session scope: runs before each permutation
  on the session's dedicated connection.
- `teardown { SQL }` — at global scope: runs once after all permutations.
- `step "name" { SQL }` — a named SQL statement that can appear in permutations.
- `permutation step1 step2 ...` — one ordered execution of steps across sessions.

Step names may be quoted (`"name"`) or unquoted (`name`).  Multi-line blocks are
supported; the `{` may appear on the same line as the keyword or on the next.

### Running a spec

```go
import (
    "context"
    "testing"

    "github.com/goopg/goopg/internal/testport/framework"
)

func TestMySpec(t *testing.T) {
    runner := &framework.IsolationRunner{
        DSN: "host=127.0.0.1 port=5432 user=postgres dbname=postgres sslmode=disable",
    }

    ctx := context.Background()

    // Run and compare against the upstream .out file.
    result := runner.RunAndCompare(ctx, repoRoot, "postgres/src/test/isolation/specs/my.spec")
    switch result.Status {
    case "pass":
        // output matched expected exactly
    case "defer":
        t.Skipf("defer: %s", result.Diff)
    }
}
```

`RunAndCompare` derives the expected file path automatically by replacing
`/specs/` with `/expected/` and `.spec` with `.out`.

To obtain raw output (without comparison):

```go
spec, err := framework.ParseIsolationSpec("/abs/path/to/my.spec")
// ...
output, err := runner.RunSpec(ctx, spec)
```

### Output format

Output mirrors `isolationtester` exactly:

```
Parsed test spec with 2 sessions

starting permutation: read read2 write done done2
step read: SELECT * FROM t WHERE id = 1;
 id | v
----+---
(0 rows)

step read2: SELECT * FROM t WHERE id = 1;
 id | v
----+---
(0 rows)

step write: INSERT INTO t VALUES (1, 'a');
step done: COMMIT;
step read2: <... completed>
 id | v
----+---
  1 | a
(1 row)

step done2: COMMIT;
```

- Blocked steps are annotated with `<waiting ...>` on the same line as their SQL.
- When a blocked step completes, a `<... completed>` line is emitted before its
  result set.
- Errors are formatted as `ERROR:  message` (two spaces after the colon).
- Numeric columns are right-aligned; text columns are left-aligned.

### Discovering all specs

```go
specs, err := framework.DiscoverIsolationSpecs(repoRoot)
// specs is a slice of relative paths like
// "postgres/src/test/isolation/specs/read-write-unique.spec"
```

---

## Regress runner

`RunRegressSubset` executes a selected set of pg_regress SQL cases and compares
output to the corresponding `expected/*.out` files via `NormalizeRegressOutput`.

```go
cases, err := framework.DiscoverRegressCases(repoRoot)
results, err := framework.RunRegressSubset(ctx, repoRoot, cases, myExecutor)
```

`myExecutor` must implement `RegressExecutor`:

```go
type RegressExecutor interface {
    ExecuteSQL(ctx context.Context, sql string) (string, error)
}
```

Return `framework.ErrDeferred` or `framework.ErrExcluded` to annotate
individual cases without treating them as failures.

---

## Status/inventory tracking

`IsolationSpecResult`, the CSV-backed `PortStatus`, and the suite inventory are
used by the `cmd/gen-oracle-*` tools and the CI oracle report.  See
`docs/test-port/postgres-oracle-port-status.csv` for the canonical defer/excluded
list and `docs/milestones/0060-postgres-oracle-test-port.md` for policy.
