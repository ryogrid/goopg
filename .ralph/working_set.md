(idle — nothing in flight)

Last loop: **M-NIGHTLY `regress/suite-wedge` — the TRIGGER half is now
INSTRUMENTED, and two of the item's own hypotheses are REFUTED.**

1. **Not host overload, not a GC-thrashing server.** Run `20260806-191958`'s
   232 cases sum to **298.5 s INCLUDING the 120 s wedge** → 178.5 s for the
   other 231, *faster* than the 302 s the same suite takes locally with no
   wedge. Nothing degrades gradually; one case stops dead, server stays fast.
2. **One statement hangs past its own 5 s `statement_timeout`** (ExecuteSQL
   sets one) ⇒ the wait is on a path that never observes the statement
   deadline. That is the defect to find.
3. Seven loops could not name it because the harness kept NOTHING:
   `framework.RegressResult` carries only a rationale string, psql's partial
   output died with the killed client, nobody looked at the server.
4. Landed the **wedge probe**: at 60 s a case captures `SELECT 1` liveness,
   `pg_stat_activity`, `pg_locks`, a `debug=2` goroutine dump filtered to
   goroutines blocked >1 minute, `/proc` RSS, and the killed client's partial
   output. Summary via `t.Log` (nightly collects only `testport/go-test.log`);
   bundle under `tmp/regress-wedge/<case>/`.
5. Side-discovery: goopg's `pg_stat_activity` emits an EMPTY `pid` for internal
   sessions → `pq: strconv.ParseInt` kills the whole query for any Go driver
   (psql hides it). Probe casts every column to `text`; filed + ledger row.

Files: new `internal/testport/regress_wedge_probe_test.go` +
`regress_wedge_probe_guard_test.go`; `regress_suite_test.go` (PprofAddr/
wedgeDir fields, `t.Setenv(GOOPG_PPROF_ADDR)`, armWedgeProbe wiring, partial
output persisted); `ci/design/02-test-selection.md` §A "Wedge-probe rule" +
`ci/design/README.md`; fix_plan (wedge item + S7 eighth-loop amendment + new
pg_stat_activity item); 2 ledger rows.

Key symbols: `armWedgeProbe`, `captureWedgeDiagnostics`, `stuckGoroutines`,
`wedgeGoroutineSection`, `reserveLoopbackPort`, `regressWedgeProbeAfter`.

Gates run: both new guards PASS (live guard proven non-vacuous — it failed on
the `pg_stat_activity` assertion before the `::text` fix); full
`TestPort_RegressSuite` PASS 194.9 s with the probe wired in (no wedge locally,
no overhead); `go vet ./internal/testport` clean; units gate PASS;
`make ralph-state-guard` (self-repaired, OK); pgbench smoke via commit hook. No
engine code changed (test harness only) ⇒ SPOT/DS05 not applicable.

NEXT LOOP (banner: M0124 closed → M0125 closed → **M0127** → M-NIGHTLY →
M0123). S7 still unmet. Run `make nightly-batch`. If a case wedges, read the
probe block out of `ci/logs/<run>/testport/go-test.log` — `pg_stat_activity`
names the statement and the long-blocked goroutine stacks name the server frame
it is parked in; fix that wait path to honour the statement deadline. If it
does not wedge, the S7 cycle may already be clean — check the rest of the run.

In-flight: none.
