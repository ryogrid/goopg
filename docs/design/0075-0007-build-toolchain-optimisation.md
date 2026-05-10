# Design 0075-0007 — Build-toolchain optimisation (PGO + GOAMD64 + linker strip + trimpath)

**Milestone:** M0075-0007
**Status:** **PARTIAL — Makefile infrastructure landed
2026-05-10; the actual optimised build produced a NET
WALL-TIME REGRESSION (+9.5 %) on this workload, so the
default flow does NOT enable the optimisation flags.
Empirical investigation of which knob is responsible is
deferred to M0076.** The build-toolchain tooling
(`bench-build-optimized`, `pgo-profile` Makefile
targets) is preserved as an infrastructure landing for
future iterations; running it produces a working binary
that just happens to be slower on the goopg dispatch-
heavy hot path. Correctness was unaffected (all 5 tight-
gate queries returned correct row counts).

**Tight-gate measurements (PGO + GOAMD64=v3 + ldflags +
trimpath vs M0075-0005 baseline `8230af8`):**

| Query | Baseline | Optimised | Δ |
|-------|---------:|----------:|---:|
| Q12   | 94 s     | 105 s     | +12 % |
| Q13   | 61 s     | 71 s      | +17 % |
| Q21   | 380 s    | 412 s     | +8 %  |
| Q22   | 59 s     | 69 s      | +17 % |
| Q9    | 219 s    | 233 s     | +6 %  |
| Total | 813 s    | 890 s     | **+9.5 %** |

Binary size DID drop as expected (14.1 MB → 9.9 MB,
−30 %). Correctness preserved (Q12=2, Q13=35, Q21=381,
Q22=7, Q9=7).

**Suspected root causes (M0076 investigation):**
- PGO inlining made dispatch HOTTER not colder. The
  captured profile (Q1+Q3+Q12+Q13+Q21) may have skewed
  the inlining decisions — pprof's own sampling
  overhead during capture distorts the function-time
  distribution.
- `GOAMD64=v3` AVX2/BMI2/FMA codegen may have
  introduced register pressure or shorter wins on
  goopg's specific hot loops (per-Datum dispatch is
  not a vectorisation target — Go's compiler can't
  vectorise the type-switch + per-Datum function calls
  in evalExprSlot).
- `-ldflags="-s -w"` strip is unlikely the cause (i-
  cache locality should help, not hurt, modestly).

**M0076 follow-up plan:**
- A/B test each knob individually:
  - Build with PGO only → measure
  - Build with GOAMD64=v3 only → measure
  - Build with -ldflags="-s -w" only → measure
- Capture a different PGO profile shape (e.g., longer
  capture window, or specific TPC-H queries that
  exercise the dispatch hot path more cleanly).
- Investigate whether `GOAMD64=v2` (less aggressive
  than v3) lands better.
**Owner:** TBD
**Branch:** `gc-oriented-refactor` (continuation)
**Depends on:** Go 1.21+ (PGO GA at 1.21).

## Context

M0074-final and M0075-0005 (commit `8230af8`) have
landed structural CPU-side optimisations (numericDiv
int64 fast-path, ColumnRef hoist, evalBinaryBatch
infrastructure). The macro Q5 CPU profile shows the
remaining hot path is dispatch-bound:
- `evalExprSlot` 72 % cum CPU
- `evalBinary` 33 % cum CPU
- `compareDatum` 13 % cum CPU

Go's compiler emits reasonable code by default but
several knobs are now generally available that improve
dispatch-bound interpreters specifically:

1. **PGO (Profile-Guided Optimization, GA in 1.21)** —
   inlining decisions across function boundaries
   informed by an actual profile. Hot paths get
   aggressive inlining; cold paths stay out-of-line.
   Typically 2-10 % wall-clock; some dispatch-bound
   interpreters see > 15 % improvement.

2. **`GOAMD64=v3`** — emits AVX2 / BMI2 / FMA
   instructions. Meaningful for hashing (CRC32,
   xxhash), checksums, vectorised string ops, and
   any code the compiler vectorises. The default
   `GOAMD64=v1` targets all amd64 CPUs back to ~2003.

3. **`-ldflags="-s -w"`** — strips DWARF debug info
   (`-w`) and symbol table (`-s`). Smaller binary;
   better i-cache locality for CPU-bound hot loops.
   Side effect: stack traces lose function names. NOT
   relevant for normal pprof which uses runtime
   metadata, not DWARF.

4. **`-trimpath`** — reproducible builds; strips
   build-host filesystem paths from the binary.

5. **Build tags for arch-specific kernels** — standard
   Go pattern for CRC / hashing / varint decoding:
   `crc_amd64.s` (assembly), `crc_arm64.s`,
   `crc_generic.go` (fallback). Selected by build
   constraints. Out of scope for M0075-0007 unless an
   existing kernel benefits — empirical evidence
   required.

## Anti-targets

- **`-gcflags="-B"`** (disable bounds checks globally).
  DO NOT USE. Bounds checks are removed by SSA escape
  analysis in hot loops already; the global flag opens
  silent OOB UB across the codebase. Listed for
  documentation; M0075-0007 does NOT enable it.

## Goals

- Add a `Makefile` (or shell script) target
  `build-optimized` that emits a binary with all the
  M0075-0007 build flags applied.
- Capture a `default.pgo` from a representative
  TPC-H workload (Q1 + Q3 + Q12 + Q13 + Q21 mixed) so
  PGO has signal on the dispatch-heavy paths.
- Document the build flag matrix + trade-offs in this
  design doc.
- Verify the optimised binary passes the M0075 pre-
  commit gate (tight gate + 21-q sweep).
- Measure wall-time delta vs the unoptimised binary
  on at least one of Q1 / Q3 / Q12 / Q13 / Q21.

## Non-goals

- **Adding new assembly kernels.** Build tags +
  `crc_amd64.s` etc. are mentioned for completeness
  but introducing them without empirical evidence the
  existing Go implementation is the bottleneck is
  premature.
- **Cross-compilation matrix.** M0075-0007 targets
  the development host (linux/amd64); ARM64 / Windows
  optimisation patterns documented for reference only.
- **Continuous PGO** (auto-regenerating profiles in CI).
  M0076+ candidate.
- **`-buildmode=pie`** — defaults vary by GOOS; not a
  perf knob.

## Proposed implementation

### Makefile target (NEW or extended)

```makefile
# Default development build (debuggable, no PGO).
.PHONY: build
build:
	go build -o tmp/goopg-bench-bin ./cmd/goopg

# Production-optimised build. Requires default.pgo at
# repo root (run `make pgo-profile` to capture).
# GOAMD64=v3 requires CPU support for AVX2 + BMI2 + FMA;
# overrideable via env var.
.PHONY: build-optimized
build-optimized:
	GOAMD64?=v3 \
	go build \
		-pgo=default.pgo \
		-ldflags="-s -w" \
		-trimpath \
		-o tmp/goopg-bench-bin \
		./cmd/goopg

# PGO profile capture. Runs a TPC-H mixed workload
# while collecting CPU samples; writes to default.pgo.
.PHONY: pgo-profile
pgo-profile:
	@echo "Building unoptimised binary for profile capture..."
	go build -o tmp/goopg-bench-bin ./cmd/goopg
	@echo "Starting server..."
	./tmp/goopg-bench-bin start \
		-D bench/tpch/runtime_goopg/data \
		--listen 127.0.0.1:65433 \
		--hba bench/tpch/runtime_goopg/data/pg_hba.conf \
		> bench/tpch/runtime_goopg/goopg.log 2>&1 &
	@sleep 5
	@echo "Capturing PGO profile (480 s)..."
	curl -s -o default.pgo \
		"http://127.0.0.1:6060/debug/pprof/profile?seconds=480" &
	@sleep 1
	./tpch-runner --queries=1,3,12,13,21 \
		--per-query-timeout=620s --cancel-after=600s
	@wait
	@echo "Profile captured: default.pgo"
	@ls -la default.pgo
```

### default.pgo capture strategy

The captured profile must cover the dispatch-heavy
paths PGO will optimise:
- `evalExprSlot` (Q1/Q3/Q5/Q12/Q13 all hit it heavily)
- MHJ build + probe (Q3/Q5/Q21)
- Aggregate path (Q1/Q3/Q14)
- IndexScan + chained NLI (Q21)

A 5-query mixed workload (Q1+Q3+Q12+Q13+Q21) covers all
these paths in ~10 minutes total wall time. The
480 s pprof window samples enough of each query.

### .gitignore consideration

`default.pgo` is typically 1-5 MB. Storing in the repo
is reasonable for reproducibility. If the team prefers
not to track binary blobs, add `default.pgo` to
`.gitignore` and document the regeneration flow in the
Makefile.

## Verification

Pre-commit gate (M0075 standard):
- Tight gate: Q12=2, Q13=35, Q21≥100, Q22=7, Q9≥7
  on the optimised binary.
- 21-q SF=1 sweep: zero row-count change.
- `go test ./...` PASS (the unoptimised default-build
  test pipeline; build flags don't affect test
  binaries).

Performance gate (best-effort):
- At least one of Q1 / Q3 / Q12 / Q13 / Q21 wall time
  ≤ 95 % of the unoptimised binary (≥ 5 % drop).
- Binary size: optimised ≤ 50 % of unoptimised.

Failure to hit the 5 % wall-time floor is acceptable —
the Makefile + profile capture lands as infrastructure
for M0076+ refinement.

## Risks

| # | Risk | Mitigation |
|---|------|-----------|
| R1 | PGO capture not representative → no improvement or regression | Capture covers Q1+Q3+Q12+Q13+Q21 mixed (≥ 4 of 5 dispatch-hot queries); long 480 s sample window. |
| R2 | GOAMD64=v3 produces a binary that won't run on CI hardware | Keep the default `make build` target unchanged (no v3); `GOAMD64` is overrideable via env var. |
| R3 | `-ldflags="-s -w"` breaks pprof symbol resolution | pprof uses runtime symbol metadata, not DWARF; verify by capturing a profile from the optimised binary and confirming function names render. |
| R4 | PGO profile becomes stale as code changes → silent un-optimisation | Document regeneration cadence in Makefile; M0076 candidate for CI auto-regeneration. |
| R5 | Build size reduction breaks debugging tooling that needs DWARF | Default `make build` unchanged; -ldflags only on `build-optimized`. |
| R6 | Inlining changes from PGO break a subtle correctness invariant | 21-q sweep parity gate catches; run `go test ./...` against the optimised binary as defence-in-depth. |

## Migration plan

Single commit (Commit D2 in M0075):
1. Add `Makefile` with `build`, `build-optimized`,
   `pgo-profile` targets.
2. Run `make pgo-profile` to capture `default.pgo`.
3. Run `make build-optimized` to build the optimised
   binary.
4. Pre-commit gate: tight gate + 21-q sweep on the
   optimised binary.
5. Measure wall-time delta vs M0075-0005 unoptimised
   baseline.
6. Land Makefile + design doc + default.pgo (or
   .gitignore entry).

If wall time regresses on any query: revert the
Makefile + profile; investigate whether PGO inlining
broke a hot path. Carry to M0076 with a research
finding.

## References

- Go 1.21 release notes (PGO GA): https://go.dev/blog/pgo
- `GOAMD64` env var: Go runtime documentation.
- Build tag pattern reference: `crypto/sha256/sha256block_amd64.s`
  in the standard library.
- M0075-0005 (commit `8230af8`) — unoptimised baseline
  for wall-time comparison.
