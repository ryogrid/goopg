# Working set — M-NIGHTLY AI-20260817-011734-006 (LANDED `acd9b161`)

**Task:** M-NIGHTLY nightly triage, item -006
(`TestPort_RegressWedgeProbeNamesTheStuckStatement`). Selected per the Current
Priority banner (M-NIGHTLY outranks M0134).

**Landed (test-only, 1 file, zero production-code change):** the guard test's
check (3) asserted the goroutine dump contained the package path
`internal/server` — **a package that does not exist in this module**. The
postmaster/connection code is `internal/postmaster`; the many `internal/server`
mentions across the tree are stale COMMENTS from an old rename. The test is new
(`b0b4dc61`) and had never passed. Fix: match the module import prefix
`github.com/goopg/goopg/internal/` instead of a specific package name.

**Ruled out (do not re-investigate):** the probe's pprof plumbing. It correctly
GETs `/debug/pprof/goroutine?debug=2` from the goopg SERVER subprocess
(`regress_wedge_probe_test.go:274 fetchGoroutineDump`; listener
`cmd/goopg/main.go:338-352`; addr via `GOOPG_PPROF_ADDR`, `…guard_test.go:41-42`).
Verified: the dump holds 35 `github.com/goopg/goopg/` frame lines incl.
`internal/postmaster.(*Server).acceptLoop`, `xlog.(*Checkpointer).Run`,
`storage.(*Bgwriter).run`, `control.(*Listener).Serve`.

**Trap that misled the previous loop's triage (and nearly this one):** the
failure message prints `truncate(dump, 400)`, and EVERY `debug=2` pprof dump
legitimately opens with the requesting goroutine's own
`runtime/pprof.writeGoroutineStacks` frames. That prefix is NOT evidence of an
in-process/wrong-process capture. The researcher's "stale assertion" hypothesis
was not taken on trust — the implementer had to prove goopg frames were present
(brief Step 0, BLOCKED otherwise) before any edit.

**Files:** `internal/testport/regress_wedge_probe_guard_test.go`,
`.ralph/fix_plan.md`.

**Gates run:** `go build ./...` PASS; `go vet ./internal/testport/` PASS; target
test 3/3 PASS (FAIL-pre) + `TestRegressWedgeProbeStuckFilter` PASS;
`RALPH_PRECOMMIT_SCOPE=units` PASS (warm cache, 3.3 s); pre-commit pgbench smoke
PASS (12.7k TPS). No tpch-spotcheck / design doc — test-only, no subsystem change.

**Deferral ledger:** no row. Nothing PG-semantic was left unimplemented; this was
goopg-internal test tooling. (Open hygiene item, NOT a deferral: stale
`internal/server` comments remain across `internal/executor/*` etc. Only comments
— grep confirmed no other code/assertion depends on the dead name.)

**Next step:** M-NIGHTLY run `20260817-011734` has ONE unchecked item left:
**AI-20260817-011734-001** (race/internal/initdb), still expected STALE — the run
predates `83dd7ae8`. Verify with
`make race-gate RACE_TIMEOUT=45m RACE_SHARD_ONLY=1` when a ~20 min slot is free;
if green, close it as stale with no code change. After that M-NIGHTLY is drained
and the banner routes to **M0134** (regress-sql `failed`/`not-tried` digestion).

**Delegation:** `tmp/ralph-handoffs/m-nightly-20260817-wedge-probe-frames/`
(researcher 1 round, implementer DONE 1 round, tester units gate DONE 1 round).

**In-flight:** none.
