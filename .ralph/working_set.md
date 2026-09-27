# Working set (loop #19 → next)

Task: M0146-0009a (recon, done this loop) — Q7/Q27 `cd+dd+item+ss`
estimate-collapse root cause; fix filed as M0146-0009b.

## Files touched
- `.ralph/fix_plan.md` — 0009a marked [x] with nested findings; filed
  `M0146-0009b` (impl, bpchar MCV-probe fix) under it
- `docs/design/0100-0149/m0146-0009a-bpchar-mcv-eqsel-miss.md` — new
- `docs/design/README.md` — index row added
- `analysis/m0146/m0146-0009a/` — README + Q7 captures (goopg EA, PG
  EXPLAIN, DPREL joinrel sizes)
- `tmp/m0146-0009a/` — private SF0.25 clone (server stopped, dir kept)

## Key symbols
- `eqSelectivityForColumn` (internal/optimizer/selectivity.go:376) —
  byte-equal `mcv.Value == literal` probe at :385
- `formatExprConstant` (:964) — renders `*StringConst` unpadded
- `formatDatumDateStyle` (internal/executor/operators_analyze.go:1534) —
  stamps MCV.Value blank-padded for bpchar(N)
- `eqJoinSelectivityExt` (joinselectivity.go:316) — confirmed PG-exact

## Findings
- Root cause: `cd_education_status` is char(20); MCV entries are padded
  ("College             ") but literals aren't → eq sel misses MCV →
  remainingDistinct=0 → DEFAULT_EQ_SEL 0.005 vs true 0.1418 (28.4x).
- cd 3-filter rel: goopg 980 vs PG 28,038 (actual 27,440). char(1)
  conjuncts (gender/marital) hit MCVs fine — only char(N>1) misses.
- Cascade: {cd+ss} 351 vs PG ~10,046 → {cd+dd+ss} 2 → {cd+dd+item+ss} 2.
  Join math verified PG-exact; upstream {ss+dd} under-est (3422 vs actual
  136830) is a SHARED PG error, not goopg-specific.
- Q27 = same mechanism (`='Secondary'`). Plan shapes identical to PG.
- PG oracle on :65438: db `tpcds025`, user **`ryo`** (no `postgres` role).

## Next step
Commit + push this loop's recon closeout. Next loop per banner:
M0146-0009b (impl — bpchar-aware MCV compare; thread column type into
eqSelectivityForColumn; sibling sites :221/:370/:1184, histCmp :643,
MCV-MCV pairing cardinality.go:1224/:2169).

## Gates run
- No production change (recon) — commit gate = docs/evidence only
- Parity scorer confirmed live Q7 finding at HEAD: Gather Merge
  relset-full, qerr 952 (est 2 vs actual 1904, PG est 15)

## In-flight
- none (private server stopped)
