# M0125-0047 — evidence

Full write-up: `docs/design/0125-0047-joinorder-tiebreak-determinism.md`.

The defect: `pickNextByEdge` (`internal/planner/joinorder.go`) ranked
join-order candidates while ranging over `edges[j]`, a `map[int]struct{}`,
with a **strict** `rowCounts[k] < rowCounts[best]`. A strict comparison keeps
the incumbent on a tie, so the winner was whichever candidate Go's per-`range`
randomiser yielded first. TPC-DS Q85 scans `customer_demographics` twice
(`cd1`/`cd2`), and two aliases of one table have identical statistics by
construction, so the tie — and the flip — were unavoidable.

## Artefacts

| path | what |
|---|---|
| `capture-plans.sh <arm> <binary>` | 96-query SF0.5 `EXPLAIN` capture, one arm per server start (~2m43s/arm) |
| `before1/ before2/` | pre-fix binary, 2 restarts |
| `after1/ after2/ after3/` | fixed binary, 3 restarts |
| `probe-q85-restarts.sh <binary> <label> [N]` | restarts the server N times and records only Q85's alias order + plan digest |
| `probe-before.txt` `probe-after.txt` | the 10-restart probe, both binaries |

## Results

96-query capture:

- `after1` vs `after2` vs `after3` — **all 96 byte-identical pairwise**
  (the item's stated 3-restart acceptance).
- `before1` vs `after1` — **96 byte-identical**. The fix converges on the plan
  the baselines already hold (Q85 keeps `cd2`-first, digest
  `6fb943ca2c7aa936`), so **no snapshot needs re-pinning**.

10-restart Q85 probe:

- pre-fix: restart 1 `cd1,cd2`, restarts 2–10 `cd2,cd1` — **1 divergence in 10**.
- fixed: `cd2,cd1` ×10 — **0 divergences**.

The two binaries differ in nothing but the tie-break comparison, so this reads
causally.

## Read the probe honestly

On its own the restart probe is **underpowered**. The observed flip rate is
~10%, not 50%, so ten clean post-fix restarts would happen by chance about a
third of the time even if nothing had been fixed. The probe's job is to show
the defect still reproduced at HEAD — it had only ever been seen on a
commit-4-era binary — and that it is gone from the fixed one.

The *proof* is `internal/planner/joinorder_determinism_test.go`: Go
re-randomises map order on every `range`, not once per process, so 200
in-process iterations sample the randomiser 200 times, where 10 restarts
sample it 10 times at ~90 s each. All four guards there were proven to FAIL
against the pre-fix body before the fix landed.
