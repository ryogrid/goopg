# Roadmap, PoC, and Risks

Date: 2026-05-27

A phased path from the current "extensions out of scope" state to a measured,
leaf-first capability. Each phase has a concrete deliverable and a gate; later
phases are conditional on earlier results.

## Phases

### P0 — Classifier and triage (smallest, do first)
- Build `cmd/gen-contrib-footprint` per
  [03-symbol-footprint-classifier.md](03-symbol-footprint-classifier.md); emit
  `docs/test-port/contrib-footprint.{csv,md}` over all 48 installable extensions.
- Human-review the ~5–15 LEAF rows and the `needs-patch` set.
- **Gate:** a reviewed porting queue exists. No engine changes yet.
- **Value:** converts the tiering in
  [02-scope-mechanisms-and-tiers.md](02-scope-mechanisms-and-tiers.md) from
  estimate to measurement; decides which extensions are even candidates.

### P1 — Framework foundation (shared, unavoidable)
- `CREATE EXTENSION` install runtime: `.control`/`.sql` execution,
  `pg_extension`/`pg_depend` population, `MODULE_PATHNAME` → native-provider
  resolution.
- Binding-keyed function dispatch replacing the `expr.go` name-`switch`; accept
  `LANGUAGE C` by binding `(module, symbol)` → a registered Go function.
- The custom goopg SDK header set (macro layer) from
  [01-approach-cxgo-sdk-shim-marshaling.md](01-approach-cxgo-sdk-shim-marshaling.md).
- **Gate:** `CREATE EXTENSION intagg;` (pure-SQL) succeeds end-to-end on
  unmodified artifacts.

### P2 — Leaf PoC (prove both porting methods)
- `fuzzystrmatch` via **cxgo+shim** (Stages 1–4): SDK headers → cxgo transpile →
  Go shim (`palloc` arena, `ereport`→error, varlena builders) → scalar
  marshaling.
- `citext` via **native-go-port** (leaf, clean, small — the matrix's
  native-port case).
- **Acceptance gate:** run each extension's **own** `sql/` + `expected/`
  regression files unmodified against goopg. This is the conformance harness from
  [02](02-scope-mechanisms-and-tiers.md) §8 and the unlock path for the
  currently-deferred `docs/test-port/` suites **D-006** (`src/test/modules`) and
  **D-007** (`contrib`).

### P3 — Measure, compare, decide breadth
- Compare cxgo+shim vs. native-port on the leaf set by: lines of hand-written
  glue, transpile-patch effort, shim symbol-set growth, maintenance burden, and
  regression-test pass rate.
- Decide whether to widen (more Tier 1 leaves; begin the type/operator framework
  for Tier 2) or stop. Feed measured costs back into the classifier's
  `--native-loc-max` / `--macro-density-max` thresholds.

## Risks and non-goals

- **Shim symbol-set growth is non-linear.** Each new leaf may pull a few more
  leaf symbols; the hard gate keeps non-leaf out, but the shim must be kept
  minimal and audited. The classifier's `symbol_buckets` column tracks this.
- **`unsafe.Pointer` in cxgo output is not fully memory-safe.** A transpiled
  bug is a recoverable panic *at best*, but pointer misuse can still corrupt
  memory. Keep transpiled code behind clear boundaries and fuzz scalar I/O.
- **Two sources of truth for an algorithm.** A cxgo-ported function tracks the
  upstream C; a native-ported one diverges. Record the upstream commit/path in a
  comment for cxgo ports so re-transpilation is reproducible.
- **Physical-format-dependent extensions never become unmodified-compatible**
  (`pageinspect`, `pg_walinspect`, `amcheck`): goopg's on-disk/WAL formats differ
  from PG. Excluded by policy, not by effort.
- **Scope boundary.** `.ralph/specs/GOAL_AND_REQUIREMENTS.md` lists extensions as
  out of scope. This bundle is exploratory analysis. Greenlighting any phase
  beyond P0 requires a `docs/design/<id>-NNNN-*.md` document and an update to the
  requirements scope, per `.ralph/AGENT.md` discipline. P0 itself is a read-only
  analysis tool and does not change engine behavior.

## One-line summary

Build the classifier first; it is cheap and decides everything downstream. The
porting method (cxgo+shim vs. native Go) is a small per-leaf choice; the
framework is the large shared investment, and it is required even for the
trivial cases.
