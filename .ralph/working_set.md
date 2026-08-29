(idle — nothing in flight)

## Loop #6 result — M0134-0175 landed

**Nightly triage:** `ci/logs/action-items.md` still at run `20260828-235424`; both
`## AI-` items already filed (001 advisory-lock FIXED in `b479ebfd4`, 002 Q5 timeout
open). Nothing new to file.

**Baton check:** tree matched `(idle)` — zero modified `.go` files at start.

**Task:** M0134-0175 `tablesample.sql` sized live for the first time
(`not-tried` → **`failed`**, 402 diff lines / 46 `^+ERROR` / 10 `^-ERROR`)
→ **PARKED** at 304 / 6 / 3.

**Shipped: the whole TABLESAMPLE feature.** It did not exist — the keyword was in
`kwlists_gen.y:425` with a token number and the right `type_func_name_keyword`
category, but no production consumed it. Grammar (`tablesample_clause`,
gram.y:14001, **zero new conflicts**, still the pinned 59), `RangeVar.TableSample`,
`optimizer.TableSampleSpec` on `SeqScan` (resolved ABOVE the inheritance/partition
expansion so every Append leaf becomes a Sample Scan), new
`internal/executor/tablesample.go` with both samplers, all four validation errors,
and EXPLAIN's `Sample Scan` label + `Sampling:` line. Design
`docs/design/m0134-0175-tablesample.md`.

**Four things worth carrying:**

1. **PG's samplers are deterministic HASHES, not PRNG streams.** `bernoulli`
   hashes `{blockno, offset, seed}`, `system` hashes `{blockno, seed}`, both with
   `hash_any` against `rint((PG_UINT32_MAX+1)*pct/100)` held in a **uint64** (100%
   is 2^32 — narrowing it to MaxUint32 silently drops one hash). Seed is
   `hashfloat8(REPEATABLE)`, whose zero short-circuit is why `REPEATABLE (0)` is
   machine-independent. goopg already had `hash_any` as `pgHashBytesExtended`
   (`hash_partition.go`) — **no new primitive needed, and the port is exact**:
   the guard pins all three oracle row sets and they match byte-for-byte.
   Generalisation: before assuming a PG feature is unportable because it "looks
   random", check whether upstream chose a hash precisely so its regress
   expectations could be pinned.

2. **The case's residual is NOT in the code the case names.** With the sampler
   proven exact, the rows still diverged — because `fillfactor` is **never applied
   at INSERT**. It is parsed, persisted to `pg_class.reloptions`, pg_dump'd, and
   consumed by the COST MODEL, but has no consumer in the insert path, so the
   `fillfactor=10` fixture lands in 1 block where PG uses 4. Correct arithmetic
   over the wrong page layout. Filed M0134-0175a; the real cost is that
   `fillfactor` cannot reserve HOT-update space, which is why it exists.

3. **A guard flagged its own stale allowlist.** `TestReservedKeywordsReachable`
   failed with "STALE allowlist entry TABLESAMPLE: it IS consumed by a production
   now" — the exact edit to make. Worth imitating: an allowlist test that also
   detects entries that became unnecessary.

4. **Revert-check honestly, and record the one that did NOT bite.** Swapping
   bernoulli's hash word order and inverting the method/percentage check order
   both fail the suite. `math.RoundToEven` → `math.Round` does **not** — the two
   differ only at exact `.5` cutoff boundaries, which no pinned percentage
   reaches. RoundToEven is still faithful (C `rint()` is round-half-even); the
   test simply cannot discriminate, and that is written down rather than implied.

**Gates run:** `make gen-parser` (59 conflicts, unchanged); parser / executor /
optimizer / catalog / storage suites PASS; new guards
`TestTableSamplerMatchesOracle`, `TestTableSampleSeedFromRepeatable`,
`TestTableSamplerRejects`, `TestTableSampleClause` PASS (2 of 3 revert-checks
bite, see above); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 22.2s, Q13 rows=34 9.0s);
`make check-testport-inventory` + `make regen-testport` PASS;
`make ralph-state-guard` OK (auto-repaired progress marker).
**Golden corpus review artifact:** 450 lines changed and stripping
`,TableSample=∅` reproduces the previous file **byte-for-byte**.

**In-flight: none — but one gate was ABANDONED and is worth resuming.**
A 4-case regress A/B (`select join create_misc subselect`) against a HEAD
worktree **timed out twice at 2400 s** and produced no baseline; patched-side
numbers are select 270 / join 20925 / create_misc 227 / subselect 2840, and they
are recorded here as UNVERIFIED — no regression claim is made from them. Cause of
the first failure was a missing `postgres` symlink in the fresh worktree
(`pg_isready: command not found`); the second was a cold Go build. **Resume
cheaply:** a warm worktree already exists at `tmp/ab-head` (detached at
`31c14685a`) — use it instead of `git worktree add`, which pays a full 8773-file
checkout plus a cold build every time. Both temp worktrees from this loop were
removed; `git worktree list` is clean.

**Carried obligations (20th loop):** TPC-DS SF0.5 gate still NOT run (for -0156,
-0157). -0158..-0175 are parser/DDL/catalog/ACL/wire/type-input/FK/plpgsql/
pubsub/sampling-only and cannot move a TPC-DS plan.
