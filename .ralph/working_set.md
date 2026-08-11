(idle — nothing in flight)

Last loop (#112): **M0131-S4 — forward cold-start E2E** — DONE, ticked, committed,
pushed to `make-db-cluster-compat`.

M-NIGHTLY duty: `ci/logs/action-items.md` still run `20260811-014635` (12 items,
unchanged since loop #100). All already filed; the open ones stay PARKED per
banner. No new filing needed.

What landed:
- `TestE2E_PGColdStartOnGoopgDataDir`
  (`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go`). Real PG 18.3
  starts on the LIVE directory a goopg server just shut down, zero conf edits,
  and reads goopg data correctly. **`0130-0002` Guard #1 DISCHARGED** (its text
  updated in the same commit).
- Guard 7 POSITIVE: index-qualified read under `enable_seqscan = off` returns
  the right row through a goopg-authored btree — blocker #12 did NOT resurface.
- S4.5: the non-atomic non-HOT UPDATE gap did NOT surface here.
- **Four gaps measured**, each locked in FAIL-WHEN-FIXED, filed as S12-S15 with
  a ledger row each:
  F1/S12 no sort works for any type — `GetDefaultOpClass` scans `pg_opclass`
    via index **2686** (indexOK, no seq fallback); goopg leaves 2686 an empty
    root page while the heap is correct. Fix pattern = the 2754 bootstrapper.
  F2/S13 any LANGUAGE SQL builtin aborts the backend — ALL 3397 `pg_proc` rows
    carry a malformed non-NULL `proconfig` → `TransformGUCArray` assert.
    Builtins unaffected (`fmgr_isbuiltin` never reads pg_proc).
  F3/S14 `ADD COLUMN … DEFAULT` reads NULL on old rows (no fast-default);
    `attmissingval` isn't even a column in goopg's pg_attribute. pg_attrdef
    half asserted POSITIVELY and correct.
  F4/S15 goopg-`CREATE DATABASE`-minted DB PANICs `could not open critical
    system index 2662`.
- Harness: `pgcluster.Stop` was unbounded and hung a whole `go test` for 20 min
  when the postmaster sat in crash recovery — now 20 s then SIGKILL. New
  `pgcluster.PSQLCombined` returns output without `t.Fatalf`.
- Design `0131-0004` draft → accepted + §Findings; README row.

Next loop: per banner — M-NIGHTLY filing, then M0131 top-to-bottom. Next
unchecked is **M0131-S5** (`pg_rewrite` runtime index maintenance 2692/2693,
design `0131-0005`). Note S13.3 and S14.1 share a root (trailing nullable
catalog attributes) — whoever takes one should probe the other.

Gates: new test PASS; whole `^TestE2E_` family PASS (99 s); UNITS PASS;
pgbench smoke PASS via the commit hook. No executor/planner/codec change, so
tpch-spotcheck / TPC-DS SF0.5 not required (Hard-won Rule #1 not triggered).

In-flight: none
