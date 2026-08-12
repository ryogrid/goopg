(idle — nothing in flight)

M0131-S15 LANDED (loop #16), all four subtasks; S15 checked off, 1 ledger row.

Files: `internal/initdb/initdb.go` (new `refreshTemplate0Image` + its call at the
end of `Init`), new `internal/initdb/template0_image_test.go`,
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go` (S15.4 inversion),
new design `docs/design/0131-0032-template0-image-staleness.md` + README index,
fix_plan S15, 1 ledger row.

Worth carrying:
- The FILED HYPOTHESIS WAS WRONG and that was the whole discovery. S15 said the
  runtime CREATE DATABASE path "clones only what goopg needs". It does not — it
  copies template0's ENTIRE image. `base/4` itself was stale: built EARLY by
  `bootstrapPostgresDatabase` from a not-yet-bulk-loaded `base/1`, then stamped
  with metapage-only index placeholders, and never revisited by the ~40
  bootstrappers that write `base/{1,5}` explicitly.
- Cheapest decisive probe for this class of question: `goopg init` into /tmp and
  `comm`/`cmp` the three base dirs against each other and against a real PG
  cluster (`bench/tpch/runtime/pgdata`). 89 vs 149 files and 8192 vs 16384 bytes
  for `2662` answered in one command what source reading did not.
- Whole-image refresh beat enumerating PG's critical-index set: the PANIC names
  one index, but 59 files were missing and 35 stale — a set-based fix would have
  cleared the symptom and left the defect.
- CAUTION (cost me the fix once): `git checkout -- <file>` to undo a scripted
  fail-when-broken experiment ALSO discards the loop's real edits in that file.
  Snapshot to /tmp or re-apply.

Gates: `TestTemplate0ImageMatchesPostgresDatabase` PASS + proven fail-when-broken,
`TestE2E_PGColdStartOnGoopgDataDir` PASS (FAILED first in the fail-when-fixed
direction, which is the fix proving itself), whole `^TestE2E_` family PASS (82 s),
`internal/{initdb,server,catalog}` PASS, UNITS precommit PASS,
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), pgbench smoke via the commit
hook, `make ralph-state-guard` OK (auto-repaired the stale completed marker).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new. Note in passing:
`TestE2E_FailoverPGtoGoopg` (AI-…-001) PASSED in this loop's `^TestE2E_` run, so
it looks env/flaky rather than a hard regression — still PARKED per banner.

Next loop (banner = M-NIGHTLY filing, then M0131): next unchecked M0131 slice.
Candidates in file order: S9 (LARGE), S8b, S21 (LARGE), S24, S26, S27, S28
(GIN-refusal variant + uncommitted-rows assertion remain), S30.

In-flight: none.
