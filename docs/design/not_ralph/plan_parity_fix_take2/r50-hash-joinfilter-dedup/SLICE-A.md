# R50 Slice A — bpchar-family hash-safe admission (plan, 2026-09-10; review pending)

*Design: `DESIGN.md`. Step-0 pair + Q56 witnesses archived in this dir.*

## 0. Change (ONE commit, no split-brain — DESIGN §7)

`isHashSafeTypeName` (`internal/optimizer/join_exec_keys.go`): admit
`"char"`, `"bpchar"`, `"character"` alongside the existing multi-spelling
entries (`"text"`, `"varchar"`/`"character varying"`, `"bool"`/
`"boolean"` …), with a comment citing DESIGN §2 (trimmed-storage
agreement) and the exclusions that remain (float4/float8 all-collide
datumKey arm; arrays via the `IsArray` guard in `pairIsHashSafe`).

Everything else flows through existing machinery: safe pairs join `Keys`
(key encoding — including non-lead pairs like Q47-brand, via the
composite encoder) and leave `Residual` (JF text + `execResidual`
per-match eval). HC renders `HashKeys`, invariant by construction.

## 1. Census + adjudication (DESIGN §3; TODO.md R50 class (b))

Same corpora + clone discipline as R49 (`:5533`/`:5534`, pinned GUCs
`work_mem='64MB'`, `max_parallel_workers_per_gather=4`): post-slice A/B
must show the 26 target lines vanished (exact-dup) or shrunk to the
adjudicated remainder (Q47×2/Q57×2 keep arithmetic; Q59×1 keeps week
arithmetic; Q58×2 keep ranges; Q4×2/Q11×1/Q74×1 keep CASE; Q24 `<>`
stays) with ZERO other text diffs — HC-extra explicitly out of scope and
gate-failing. Machine attribution with the FIXED alternation (space
outside the group); orphans must read 0. Q24 stays shapediff (goopg hash
vs PG NL — doctrine over coincident text).

## 2. Values bind (same gates as R49)

- TPC-H canonical digest 24/24 MATCH vs `bench/tpch/baseline-digests.txt`
  (fresh capped server, `GOGC=100`); spotcheck Q12=2/Q13=34.
- TPC-DS SF0.5 sweep all-zero (executor behavior changes — the sweep
  binds here).
- Trailing-space direction-1 documentation: `('ab' vs 'ab ')` join misses
  identically pre/post (probe, not corpus — PG would match; goopg misses
  either way; UNCHANGED by this slice).
- Units: `internal/executor` + `internal/optimizer` suites green (gate
  run, no `-count=1`).

## 3. Pins (DESIGN §5 — tests first where a text/contract is pinned)

- Whitelist unit (`internal/optimizer`): char/bpchar/character safe;
  float + bpchar[] still unsafe.
- Probe graduation: `zz_r50_probe_test.go` (untracked) → HC-only pins for
  char-base + char-CTE, NULL-key char join matches nothing, exact counts
  — folded into `explain_join_cond_test.go` or a named
  `hash_join_bpchar_*_test.go`; the `zz_` file must not survive.
- Multi-key partial pin (Q59-shape doll-house, planner-picked).
- HC-invariance pin (Keys-vs-HashKeys construction argument + text).

## 4. Gate report (2026-09-10)

- Binary: `/tmp/pp2/bin/goopg-r50a` (built from this worktree,
  `DIFFERS-FROM-BASELINE` vs `/tmp/pp2/bin/goopg-r50` by `cmp`).
  Baseline corpus `/tmp/pp2/oc-ds05-r49b.txt`; post-slice corpus
  `/tmp/pp2/oc-ds05-r50a.txt` (clone `:5533`, GUCs pinned
  `work_mem='64MB'`, `max_parallel_workers_per_gather=4`, verified
  serving binary via launch.sh inode check).
- Census A/B: JF lines 64 → 42 (delta 22). All 28 in-scope lines moved
  as adjudicated — Q56×3/Q60×3/Q59×1/Q83×2/Q47×2/Q57×2/Q24×2 VANISH;
  Q4 (5→2), Q11 (3→1), Q74 (3→1) shrink to CASE; Q58 (2→2) drops
  `item_id`, keeps ranges; Q24 `<>` stays. ZERO other plan-text diffs
  (only JF + `(N rows)` + header/tmp-path/width artifacts); ZERO
  `Hash Cond:` changes (HC-extra fails the gate — held);
  join nodes 622 → 622 (no shape change); orphans 0 on both corpora
  (fixed alternation, space outside the group; NL counts as join —
  the 11 NL-JF lines are identical pre/post).
- Scope corrections vs DESIGN draft: Q47/Q57 VANISH (the numeric/
  varchar remainder was never in JF — already folded pre-slice);
  Q83×2 IN scope (same-node HC+identical-JF, PG Q83 zero-JF).
- Units: `internal/optimizer` ok (2.2s), `internal/executor` ok
  (12.5s), `go vet` clean — no `-count=1`. New pins:
  `TestExecHashKeyPlanBpcharFamily` (optimizer),
  `TestHashJoinBpchar*` ×5 (executor); both `zz_` probes deleted.
- TPC-H: Q12=2/Q13=34 canonical (clone `:5534`, GOGC=100, fresh
  capped server; `scripts/tpch-spotcheck.sh` SKIPPED — worktree has no
  TPC-H data dir — manual fallback on `/tmp/pp2/clone-tpch` with the
  worktree-built runner); digest vs `bench/tpch/baseline-digests.txt`:
  24/24 MATCH (`tpch-runner-r50a --diff`, `/tmp/pp2/dig-r50a.txt`;
  fresh capped GOGC=100 server on `/tmp/pp2/clone-tpch :5534`).
- TPC-DS SF0.5 sweep all-zero: PASS=95 (57 ck-verified) MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4 — identical to the R49b
  baseline; status-delta verdict-changes=none (runtime-only wiggles,
  total -0.2%). Ran from the main tree — the SF0.5 data dir lives
  only there — with `SF05_NO_BUILD=1 GOOPG_BIN=/tmp/pp2/bin/goopg-r50a`,
  report `/tmp/pp2/sf05-r50a-results/sweep-20260910-170614.txt`. Note:
  three background-launch attempts were OOM-guard-killed within
  minutes (box itself healthy, 18G+ free); the foreground run per the
  loop directive survived — background the digest, foreground the
  sweep.
- Landing: single commit + push per loop directive.
