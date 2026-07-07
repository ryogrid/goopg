Task: M0122-0001 backlog triage continuation (no-match+no-status entries in
unimplemented_feat.json), doc-only bookkeeping (exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only) is the only
file changed besides .ralph/progress.json (benign ralph-state-guard
auto-repair, timestamp bump only).

Key symbols: none new (pure backlog bookkeeping). This loop's cluster (indices
16-20 of the prior 21-entry no-match list) already carried code_audit notes
dated 2026-07-07 from an earlier triage pass, so this loop mainly converted
those audits into final status flips (re-verified each before flipping, did
not blindly trust the stale note):
- m0089-0002 (Pool.PinNew/Extend dirty-tracking audit) -> resolved: design doc
  was never written, but internal/autovacuum-unrelated inspection confirms
  Pool.PinNew (internal/storage/bufpool.go:1104) does set the dirty bit
  correctly, and the scale-100 pgbench symptom that motivated the audit was
  root-caused to two unrelated bugs already fixed under M0090-0001/0002. No
  live defect remains, audit's practical question is answered.
- M0092-0008 (plan cache won't help pgbench literal-substitution pattern) ->
  left OPEN: M0098-0005 shipped a real 16-shard plan cache
  (internal/server/plancache.go) wired into both simple-query
  (dispatch.go:911) and extended (dispatch_extended.go:94) paths, but its key
  is normalizeCompatSQL which preserves literal values verbatim
  (dispatch.go:1502-1509, citing M0097-0003) -> pgbench's classic client-side
  literal substitution (`WHERE aid = 42` vs `WHERE aid = 9999`) still gets a
  distinct cache key per call -> 0% hit rate for that workload shape. The
  general prepared-statement cache exists; the specific gap this entry names
  does not, so left open (a literal-normalizing cache key / query-jumbling
  scheme is the resume point if ever picked up).
- M0091-0004 (select-only perf structural fix deferred to M0092) ->
  resolved: verified all M0092 sub-milestone design docs
  (0092-0001..0092-0007, excluding 0092-0008 which is the separate plan-cache
  entry above) carry "Status: authoritative for M0092-000N implementation" /
  "authoritative for M0092 close-out", and M0093's
  docs/design/0093-0002-pgbench-remeasurement-target.md explicitly ties its
  acceptance bar ("TPS >= 1,000, M0091's original acceptance bar") to this
  same structural-fix chain; M0093 measured 2,740 TPS (already flipped
  resolved in a prior loop via a different backlog entry). Structural fix
  fully landed across the M0092 series + M0093.
- M0086 (needsVacuum always true when RowCount>0, ignores
  autovacuum_enabled/reloptions) -> left OPEN, confirmed live via direct read
  of internal/autovacuum/launcher.go:204-217: needsVacuum only checks the
  anti-wraparound XID-age branch and `tbl.Stats.RowCount > 0`, no reloption
  lookup anywhere. This IS a full milestone already
  (docs/milestones/0086-autovacuum-needs-vacuum-pg-parity.md, Status:
  planned, with two required design docs 0086-0001/0086-0002 spelled out) so
  no new deferral_ledger row needed -- it's already tracked at
  milestone-granularity, just not yet picked up.
- opTreeSlab SeqScan/Project migration -> left OPEN (unchanged from the
  2026-07-07 code_audit): PlanSeqScan is now fully concrete (commit 1953872c)
  but PlanProject still keeps an adapter over the old planner.Node pointer
  (internal/executor/plannode.go) -- half the original claim remains true.

Findings: 21 no-match entries -> 16 remaining. Of this loop's 5-entry batch:
2 flipped resolved (m0089-0002 audit, M0091-0004 structural fix), 3 confirmed
still-open (M0092-0008 plan-cache-literal-gap, M0086 needsVacuum reloptions,
opTreeSlab Project migration). Entry count 181 preserved; status-count
160->165 (64 resolved / 101 open / 16 none remaining, zero-unicode-escape
verified). Did NOT append new deferral_ledger.md rows: all 3 still-open items
are already tracked at design-doc/milestone granularity in their own docs
(docs/milestones/0086-*.md explicitly, docs/design/0092-0008-*.md explicitly,
opTreeSlab gap documented inline in plannode.go's own migration-status
header) -- a ledger row would duplicate existing tracking, not add new
information.

Next step: continue M0122-0001 -- 16 no-match+no-status entries remain.
Regen command (indices shift after each edit):
`python3 -c "import json; d=json.load(open('unimplemented_feat.json'))['unimplemented_features']; nm=[f for f in d if 'status' not in f and f.get('resolution_check',{}).get('ledger')=='no-match']; print(len(nm)); [print(i,f['feature'][:100]) for i,f in enumerate(nm)]"`
No specific next cluster pre-identified this time -- start from index 0 of
the fresh 16-entry list (regen first, indices from this loop's printout are
now stale since 5 entries were removed from the middle of the prior list).

Gates run: `make ralph-state-guard` PASS (auto-repaired 1 benign issue:
progress.status completed-marker-from-prior-loop reconciled to in_progress,
same pattern as every prior loop). JSON validity + entry-count (181
preserved, status-count 160->165, no-match count 21->16, zero-unicode-escape)
confirmed via python3 before this working_set write. Pre-commit pgbench
smoke will run automatically via `.githooks/pre-commit` on the commit below.

In-flight: none (all investigation was direct file reads/greps this loop, no
background agents or long-running gates were started).
