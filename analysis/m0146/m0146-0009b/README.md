# M0146-0009b — bpchar MCV/histogram compare fix: evidence

Task: `internal/optimizer` literal-vs-statistics comparisons apply PG's
bpchar truelen semantics (`bpchareq`/`bpcharcmp`, varchar.c:743/:909) so a
padded `MCV.Value` ("College             ") matches the unpadded literal.
Design doc: `docs/design/0100-0149/m0146-0009b-bpchar-stats-compare.md`.

## Regression witness (private SF0.25 clone, pinned ANALYZE)

`cd_education_status = 'College'`:

| | rows est |
|---|---|
| before (HEAD `e7683534c`) | 9,604 (= 1,920,800 × DEFAULT_EQ_SEL) |
| after | **272,433** |
| PG 18.3 `:65438` | ~274,098 total / actual filtered 27,440 |
| `'Secondary'` after | 277,683 |
| `'NoSuchValue'` (non-MCV) | 9,604 (unchanged fallback, correct) |

Q7: Gather Merge `rows=2` → **rows=45** (PG: 46). Q27: `rows=2` → **45**.

## Corpus movement (baseline HEAD vs staged candidate)

- `tpcds-sf025/` — fireset-gate artifacts: baseline+candidate plan captures,
  PG arms, per-query diffs, fire-set list and per-query execution statuses.
  PLAN-PARITY match **12 → 15** (Q12, Q20, Q98 new MATCH; no losses).
  Fire set: 25 changed plans (Q7,12,18,20,26,27,33,34,48,53,54,56,60,61,63,
  64,71,72,73,75,89,91,94,95,98), all PASS both arms.
- `tpcds-sf1/` — zero plan changes (no fires).
- `m0146-0009b.plans.txt` / `-pg.plans.txt` — TPC-H parallel floor capture:
  match=6 (floor ≥3 held).

## EA ratchet (real run on fresh clone, port 5541)

`make ea-ratchet` on this tree: 17 findings vs baseline 52. Target finding
cleared (`FIXED Q27:customer_demographics+date_dim+item+store_sales`; the
Q7 relset is no longer over the bar either). 5 NEW findings
(Q78 `date_dim+store_returns+store_sales+web_returns+web_sales`,
Q83 `catalog_returns+date_dim+item+store_returns`,
Q85 `customer_demographics+reason+web_page+web_returns+web_sales`,
Q85 `reason+web_page+web_returns+web_sales`, Q85 `reason+web_returns+web_sales`)
all sit on plans **byte-identical** baseline-vs-candidate — pre-existing
drift unmasked by fixing the gate environment, not introduced here. Filed
M0146-0009c + ledger rows.

## Gate-environment defects found and worked around

- `tmp/c20a/data-sf025` was a stale clone (no tpcds tables) — the gate only
  clones when the dir is absent; moved aside to `data-sf025.stale-empty`.
- `EA_PORT=5534` default collided with a foreign postgres (pid 1748354):
  `pg_isready` answered, gate captured 99 `relation does not exist` ERRORs,
  scored 0 nodes, printed `EA-RATCHET: PASS (52 fixed)` — a vacuous pass.
  Re-run used `EA_PORT=5541`. Hardening filed as M0146-0009d.

## Gates

| gate | result |
|---|---|
| `go test ./internal/optimizer/` | PASS |
| `RALPH_PRECOMMIT_SCOPE=units` | PASS |
| `tpch-spotcheck.sh` | PASS (Q12=2, Q13=33) |
| `tpch-acceptance-arm.sh on` | PASS, 24/24 digest MATCH vs m0145-0008m baseline |
| `tpcds-sf025-regression.sh sweep` | PASS 96/0/0/0 |
| `tpcds-fireset-gate.sh` | PASS (25 fires both arms, SF1 no fires) |
| floor capture `jointree-parity-capture.sh tpch` | match=6 ≥ floor 3 |
| `make ea-ratchet` | FAIL — 5 NEW pre-existing findings (filed 0009c) |
