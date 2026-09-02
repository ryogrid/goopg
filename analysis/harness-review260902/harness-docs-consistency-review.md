# Harness Document Consistency Review

**Review date:** 2026-09-02
**Files reviewed:**
- `.ralph/PROMPT.md` (420 lines) — Ralph development instructions (PROMPT)
- `.ralph/AGENT.md` (528 lines) — Agent build/run instructions (AGENT)
- `CLAUDE.md` (84 lines) — Session guidance (CLAUDE)
- `.ralph/fix_plan.md` (487 lines) — Prioritised TODO list (FIXPLAN)

**Methodology:** Cross-file comparison of policy statements, references, file listings, and configuration values. Each finding is classified as a **Contradiction** (two files disagree on the same fact), **Duplication** (substantially overlapping content that should be consolidated), **Missing Anchor** (a reference target that does not exist in the referenced file), or **Stale/Inaccurate** (content that no longer reflects the repository).

---

## Executive Summary

- **6 contradictions** (1 missing anchor, 2 priority-ordering gaps, 1 stale file structure, 1 memory-limit discrepancy, 1 commit-while-red policy conflict, 1 pgbench-by-hand instruction conflict)
- **5 duplications** (parser-playbook instruction, `-count=1` policy, oracle test-port documentation, gate-command descriptions, headless-execution warning)
- **1 typo**

---

## Contradictions

### C1 — Missing anchor: `## Current Priority` banner does not exist in `fix_plan.md`

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 15 | `"Which milestone you then WORK is decided solely by the \`## Current Priority\` banner in .ralph/fix_plan.md"` |
| AGENT | 424 | `"unless **the \`## Current Priority\` banner** or a dependency forces another order"` |
| FIXPLAN | — | No `## Current Priority` heading exists anywhere in the file |

Both PROMPT.md and AGENT.md name `## Current Priority` as the **sole ordering authority** for task selection. `fix_plan.md` has no such heading. The bold-text paragraph at the top of `fix_plan.md` (lines 1–12) serves as the de facto priority statement, but the heading itself is missing. A reader (or agent) scanning for the anchor heading will not find it.

**Severity:** Medium — the anchor is a convention; the functional priority statement is present. However, the heading should exist as the documented reference point to avoid confusion.

**Recommendation:** Add `## Current Priority` as a heading above the priority paragraph in `fix_plan.md`, or update PROMPT.md/AGENT.md to reference the actual heading that exists.

---

### C2 — Priority-ordering chain is distributed across three sites in `fix_plan.md` with no single authority

`fix_plan.md` expresses the milestone ordering in three places:

| Location | Lines | Stated priority | Context |
|----------|-------|-----------------|---------|
| Opening paragraph | 1–12 | M-NIGHTLY → M0134 (exhausted) → M0119 | Current (2026-09-01) |
| M0131 section | 197–204 | M-NIGHTLY → M0132 → M0131 → M0119 → M0122 | Written before M0134 was filed; does not mention M0134 |
| M0134 section | 236 | "next after M-NIGHTLY (user directive 2026-08-15)" | Written when M0134 was filed |

The M0131 section (197–204) was written before M0134 existed and has not been updated to mention it. The opening paragraph (1–12) says M0134 is "EXHAUSTED" and falls through to M0119, but the M0134 section header (236) still says "next after M-NIGHTLY". The M0131 section's ordering omits M0134 entirely and places M0132 immediately after M-NIGHTLY, contradicting the M0134 section's "next after M-NIGHTLY" claim. Additionally, the M0131 chain lists M0130 ("after M0132, before any remaining **M0130**, M0119 or M0122 item") which the review's original table omitted, and **M0132 is now archived complete** (fix_plan.md:234), making the M0131 section's "M0132 is next-priority" claim doubly stale.

**Severity:** Medium — the opening paragraph is the most recent and should be authoritative, but the stale M0131 section creates a misleading alternate priority chain.

**Recommendation:** Update the M0131 section (197–204) to acknowledge M0134's position in the ordering (and M0132's archival), and harmonise the priority statement across all three sections to a single source of truth.

---

### C3 — PROMPT.md File Structure section (`src/`, `examples/`) does not match the actual repository layout

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 397–400 | `src/: Source code implementation`, `examples/: Example usage and test cases` |
| AGENT | 69–91 | Actual layout: `cmd/goopg/`, `internal/`, `docs/design/`, `postgres/`, `.ralph/` |

The PROMPT.md File Structure section (390–401) describes a conventional `src/` + `examples/` layout that does not exist in the goopg repository. The actual layout (accurately documented in AGENT.md:69–91) uses Go's standard `cmd/` + `internal/` structure. Neither `src/` nor `examples/` directories exist in the repo.

**Severity:** Medium — a stale template section that misleads new agents about the codebase layout.

**Recommendation:** Replace the PROMPT.md File Structure section with the actual layout from AGENT.md:69–91, or delete it and point to AGENT.md as the authoritative source.

---

### C4 — GOMEMLIMIT recommendation differs between AGENT.md and CLAUDE.md

| File | Lines | Value |
|------|-------|-------|
| AGENT | 11 | `export GOMEMLIMIT=15GiB` (session start) |
| CLAUDE | — | Q21 required `GOMEMLIMIT=12GiB + GOGC=100` to avoid OOM |

AGENT.md mandates `GOMEMLIMIT=15GiB` at session start. CLAUDE.md documents that TPC-H Q21 needed `GOMEMLIMIT=12GiB + GOGC=100` to complete (a lower limit). While the contexts differ (generic session start vs. a specific heavy query), the two numbers are not reconciled and a reader may apply the wrong limit.

**Severity:** Low — the values are in different contexts; no direct contradiction in the same scope. Worth noting.

**Recommendation:** Add a note in AGENT.md that heavy queries may need lower limits (reference CLAUDE.md's Q21 experience).

---

### C5 — Commit-while-red policy conflict between PROMPT.md and AGENT.md

| File | Lines | Policy |
|------|-------|--------|
| PROMPT | 216–221 | "if a pre-commit gate … fails and the fix drags on across multiple turns, **commit and push at a natural checkpoint** … the tree must build and the in-progress fix must be at a coherent stopping point. Do not let days of uncommitted WIP accumulate behind a red gate." |
| AGENT | 304–319 | "Fix it **even when the failure is unrelated** … Fold the fix into the **same commit** … so the tree is never committed while red" and "**Never commit while this suite is red.**" |

PROMPT.md permits committing through a red gate when the fix drags on. AGENT.md forbids it unconditionally. The two policies give opposite instructions on the same scenario. PROMPT.md also adds "push to origin" (:417), which no other file mentions.

**Severity:** High — direct contradiction on a critical workflow decision.

**Recommendation:** Decide which policy wins and update the other file to match. The PROMPT.md policy (commit through a red gate to avoid uncommitted WIP accumulation) is the more pragmatic one for long-running autonomous loops, but it contradicts AGENT.md's explicit "never commit while red" rule.

---

### C6 — Pgbench-by-hand instruction conflict between PROMPT.md and AGENT.md

| File | Lines | Instruction |
|------|-------|-------------|
| PROMPT | 73–76 | Lists pgbench among the gates to "run in the FOREGROUND" alongside `ralph-precommit-test.sh`, `tpch-spotcheck.sh`, `make race-gate` |
| AGENT | 235–238 | "**you do NOT run pgbench by hand — that would run it twice** (once manually, once in the hook)." |

PROMPT.md tells the agent to run pgbench manually in the foreground. AGENT.md tells the agent never to run it manually because the pre-commit hook already does. These are direct, irreconcilable instruction conflicts.

**Severity:** High — an agent following PROMPT.md will run pgbench twice (once manually, once in the hook), wasting time and potentially causing flakiness.

**Recommendation:** Align the two: either remove pgbench from the PROMPT.md foreground-gate list (deferring to the hook), or update AGENT.md to no longer forbid manual runs.

---

### C7 — Stale "M0132 next-priority" claim in AGENT.md

| File | Lines | Claim |
|------|-------|-------|
| AGENT | 425–426 | "as of 2026-08-13 it ranks M-NIGHTLY first … **with M0132 as the next-priority milestone**" |
| FIXPLAN | 234 | M0132 is archived complete |

AGENT.md still names M0132 as the next-priority milestone after M-NIGHTLY, but M0132 has been completed and archived (fix_plan.md:234). M0134 superseded it (2026-08-15), then M0134 was declared exhausted (2026-09-01) with M0119 active. This is the same stale priority chain the review flagged inside fix_plan.md's M0131 section (C2), but AGENT.md itself carries the same stale statement.

**Severity:** Medium — a stale priority statement that will mislead an agent reading AGENT.md for task selection.

**Recommendation:** Update AGENT.md:425–426 to reflect the current priority chain (M-NIGHTLY → M0134 (exhausted) → M0119, or whatever the current ordering is).

---

## Duplications

### D1 — Parser playbook §12 instruction (major duplication)

Both PROMPT.md and AGENT.md contain a lengthy, nearly identical instruction about reading `docs/design/not_ralph/06-goyacc-parser-playbook.md §12` before touching the parser:

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 156–171 | Hard-won Rule #6 — `docs/design/not_ralph/06-goyacc-parser-playbook.md` |
| AGENT | 161–209 | "Parser code — READ THE PLAYBOOK FIRST" — same content with `TYPEDLIT`, `CHECKBODY`, `*_LA`, `$<p>N`, `lastConsumedPos()`, `parity_goldens.txt`, `make gen-parser` |

The two blocks cover the same ground (synthetic terminals, position conventions, parser divergence list, build step). The AGENT version is more detailed (49 lines, lines 161–209) but the PROMPT version (16 lines) replicates the same key points. This duplication means any update to the parser rules must be made in both files or they will drift.

**Severity:** Medium — the duplication itself is not harmful, but maintenance burden is real.

**Recommendation:** Keep the full instruction only in AGENT.md (the build/run instructions file) and replace the PROMPT.md version with a concise cross-reference: "See Hard-won Rule #6 in `.ralph/AGENT.md`".

---

### D2 — `-count=1` prohibition (triplicate)

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 77–80 | `"Never pass \`-count=1\` to a gate's \`go test\`"` |
| AGENT | 255–259 | `"NEVER pass \`-count=1\` to a gate's \`go test\`"` |
| CLAUDE | — | Same policy |

Three files state the same rule. The duplication is consistent (no contradiction), but policies that appear in three places are harder to update.

**Severity:** Low — content is consistent and short.

**Recommendation:** Keep the authoritative statement in one file (AGENT.md) and cross-reference in the others.

---

### D3 — Oracle test-port / CSV / D-001..D-004 documentation (major duplication)

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 223–280 | Full section: status vocabulary table, CSV explanation, D-001..D-007 table, promotion workflow |
| AGENT | 120–159 | TestPort_ commands, CSV reference, D-001..D-004 unlock conditions |

Both files document the same oracle test-port infrastructure. The CSV status vocabulary table (`port`/`pass`/`failed`/`not-tried`/`defer`/`excluded`) exists only in PROMPT.md; AGENT.md merely references `status=defer` once (line 141). The D- prefix tables differ: PROMPT.md covers D-001..D-007, AGENT.md covers D-001..D-004.

**Severity:** Medium — substantial duplication (~60 lines each) that will drift over time.

**Recommendation:** Consolidate the canonical description into one file (AGENT.md, as the build/run instruction file) and have PROMPT.md reference it instead of duplicating.

---

### D4 — Gate commands (`ralph-state-guard`, `make install-hooks`, pre-commit smoke) distributed across three files

| Command | PROMPT | AGENT | CLAUDE |
|---------|--------|-------|--------|
| `make ralph-state-guard` | 23, 214, 297–299 | 112–114 | — |
| `make install-hooks` | 288–290 | 220–233 | — |
| `RALPH_PRECOMMIT_SCOPE=units` | — | 240–242 | Yes |
| `scripts/tpch-spotcheck.sh` | 73 | 274–280 | Yes |

Gate commands appear in all three files with varying levels of detail. The same command (`make ralph-state-guard`) is mentioned in three separate places within PROMPT.md alone. Note: `RALPH_PRECOMMIT_SCOPE=units` does NOT appear in PROMPT.md — it exists only in AGENT.md (240–242) and CLAUDE.md. The review's original table incorrectly placed it at PROMPT:214 (which is a `make ralph-state-guard` mention); corrected here.

**Severity:** Low — commands are consistent; redundancy is organisational.

**Recommendation:** Consolidate gate-command reference into a single table in one file (AGENT.md) and cross-reference from the others.

---

### D5 — Headless execution / background-task warning

| File | Lines | Content |
|------|-------|---------|
| PROMPT | 66–91 | Full "Headless Execution Reality" section with kill timing, background-task death, gate policy |
| AGENT | 261–265 | Summary note referencing PROMPT.md |

The AGENT.md version is a cross-reference to PROMPT.md, which is the correct pattern. However, the AGENT.md version still contains a condensed version of the explanation (foreground-gate rule, `-count=1` policy) that duplicates PROMPT.md.

**Severity:** Low — the cross-reference pattern is correct; the duplication is small.

**Recommendation:** No change needed; the cross-reference in AGENT.md is appropriate.

---

## Typo

### T1 — PROMPT.md:416: "VESION CONTROL RULES"

Should be **"VERSION CONTROL RULES"**.

**Severity:** Trivial.

**Recommendation:** Fix the typo.

---

## Summary Table

| ID | Type | Severity | Files | Summary |
|----|------|----------|-------|---------|
| C1 | Missing anchor | Medium | PROMPT/AGENT/FIXPLAN | `## Current Priority` heading does not exist in `fix_plan.md` |
| C2 | Contradiction | Medium | FIXPLAN | Priority ordering distributed across 3 sites with no single authority; M0131 section stale (omits M0134, names archived M0132) |
| C3 | Stale content | Medium | PROMPT | File Structure lists `src/`/`examples/` that don't exist |
| C4 | Value mismatch | Low | AGENT/CLAUDE | GOMEMLIMIT 15GiB vs 12GiB |
| C5 | Contradiction | High | PROMPT/AGENT | Commit-while-red policy: PROMPT permits committing through a red gate; AGENT forbids it |
| C6 | Contradiction | High | PROMPT/AGENT | Pgbench-by-hand: PROMPT says run it in foreground; AGENT says never run by hand |
| C7 | Stale content | Medium | AGENT/FIXPLAN | AGENT:425-426 names M0132 next-priority, but M0132 is archived complete |
| D1 | Duplication | Medium | PROMPT/AGENT | Parser playbook §12 instruction in both files |
| D2 | Duplication | Low | PROMPT/AGENT/CLAUDE | `-count=1` prohibition in 3 files |
| D3 | Duplication | Medium | PROMPT/AGENT | Oracle test-port docs overlap (D-001..D-007 vs D-001..D-004); vocabulary table only in PROMPT |
| D4 | Duplication | Low | PROMPT/AGENT/CLAUDE | Gate commands distributed across 3 files |
| D5 | Duplication | Low | PROMPT/AGENT | Headless-execution warning (cross-ref pattern correct) |
| T1 | Typo | Trivial | PROMPT | "VESION" → "VERSION" |

---

## Recommendations

1. **C1:** Add `## Current Priority` heading to `fix_plan.md` (or update all references to the actual heading).
2. **C2:** Update the M0131 section and M0134 section in `fix_plan.md` to agree on a single ordering chain (note M0132 is archived).
3. **C3:** Replace the PROMPT.md File Structure with the actual layout from AGENT.md.
4. **C5:** Reconcile the commit-while-red policies between PROMPT.md and AGENT.md (decide which wins; the AGENT.md "never commit while red" rule currently forbids the PROMPT.md "commit at a natural checkpoint" practice).
5. **C6:** Align the pgbench-by-hand instructions (remove pgbench from PROMPT.md's foreground-gate list, or relax AGENT.md's blanket prohibition).
6. **C7:** Update AGENT.md:425-426 to the current priority chain (M-NIGHTLY → M0134 exhausted → M0119).
7. **D1, D3:** Consolidate duplicated content into AGENT.md and cross-reference from PROMPT.md.
8. **T1:** Fix the typo in PROMPT.md.