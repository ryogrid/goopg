(idle — nothing in flight)

Last loop: **M0125-0006 COMPLETE**, committed + pushed as `7b436af5`.
Nightly triage done: `ci/logs/action-items.md` unchanged since 2026-07-25
(mtime Jul 25 03:20), all 26 items already filed — filing was a no-op.

## Why M0125-0006 and not M0124 / -0004 / -0003 (do not re-derive)

Banner order is M0124 → M0125, but everything above M0125-0006 was blocked:
- `M0124-0002` (timed A/B) and `M0124-0004` — need a QUIET host. **The nightly
  wedge is STILL there** (see below), so both stay unselectable.
- `M0125-0004`, `-0002`, `-0005` all diff against
  `plan_snapshots/tpcds-round2-head.txt`, which is **M0124-0002's deliverable
  and does not exist** — verified by `ls plan_snapshots/` (17 labels, not that
  one). They cannot be graded, so they cannot be worked.
- `M0125-0003`'s deliverable is a four-arm TIMED study → same host blocker.
- `M0125-0006` was the topmost item accepted **by value**, so host contention
  cannot void it. Its engine-commit freeze had already lifted (M0125-0009).

## ⚠ STILL BLOCKED ON THE USER — the nightly wedge is UNCHANGED

Run `20260729-002344` wedged since ~02:07; `goopg-bench-bin` PID **2621153**
was at 53 % CPU / 7.5 GB RSS, 11 h elapsed, at the start of this loop.
`kill` of non-owned PIDs is hard-denied by the classifier, so I cannot clear it.
Until it is gone, **M0124-0002 must not be selected**.

```
kill -TERM 2511542    # run-nightly.sh   — stops before stage-tpcds
kill -QUIT 2621153    # goopg server     — QUIT, not KILL: untrapped, so it
                      # dumps the leaked backend's stack to
                      # ci/logs/20260729-002344/tpch/server.log
```
Re-check with `pgrep -af ci/batch`.

## Facts the next loop should NOT re-derive

- **The SF0.5 gate DOES see wrong values by checksum** (not just row counts):
  Q87 was `CKMISMATCH` at HEAD and is now `PASS`. Gate moved
  `PASS=74 CKMISMATCH=4` → `PASS=75 CKMISMATCH=3`, Q87 the ONLY per-query change
  in 99 (normalised diff vs `sweep-20260729-093056.txt`).
- **`make plan-diff LABEL=r5-default` is worthless on a fresh server**: that
  baseline was captured stats-loaded, a fresh server is S-cold, so all 22 differ
  on `(stats)`/`rows=` alone. Use a same-state pre/post A/B instead.
- **TPC-H has ZERO set operators**; the plan-snapshot harness covers only TPC-H
  Q1–Q22. Only 5 TPC-DS queries have a `)` before a set operator (Q8 Q14 Q23 Q49
  Q87), and Q8's is an `IN`-list paren.
- Four NEW items filed with ledger rows, all confirmed identical on pre-fix HEAD:
  **M0125-0016** (fold has no operator precedence — bare
  `A UNION B INTERSECT C` is wrong), **M0125-0017** (`ORDER BY`/`LIMIT` in a
  parenthesised FIRST branch is hoisted to the whole result and silently DROPS a
  branch), **M0125-0018** (IN-list/EXISTS reject a parenthesised chain),
  **M0125-0019** (`string_agg(… ORDER BY …)` ignored).
- The SF0.5 sweep's Q5 pins the server at **21 GB RSS** and left the host at
  0 GB available; killing the script ORPHANS that server — stop it with
  `tmp/goopg-bench-bin stop -D <repo>/bench/tpcds/runtime_goopg/data-sf05`.
- **Never `pkill -f`** — it self-matched and killed my own shell (exit 144).
- A `cd` inside a compound Bash command PERSISTS to later calls; it silently
  moved the cwd into `bench/tpcds/.../queries` mid-loop.

Gates run: units PASS; `tpch-spotcheck.sh` PASS (Q12=2, Q13=35); SF0.5 gate
PASS=75/CKMISMATCH=3 (Q87 the only change); TPC-H plan A/B 22/22 identical;
26 new tests PASS and **proved to FAIL at `6c5c48ae`** (10 subtests); gofmt clean
on my hunks (all 3 files already dirty at HEAD elsewhere — never `gofmt -w`);
pgbench smoke PASS via hook (505 / 689 / 13826 tps); `make ralph-state-guard` OK
(auto-repaired a stale marker).
In-flight: none.
