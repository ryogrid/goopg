Task: M-NIGHTLY triage (re-confirmed no new work: ci/logs/action-items.md's
run 20260707-000712 is the same run that already seeded all 8 fix_plan tasks,
all 8 now checked + landed, gates green). Actual loop work: M0122-0001 backlog
triage / re-verification pass (doc-only, exempt from design-doc rule).

Files: unimplemented_feat.json (surgical Edits only, 35 entries given a
`status` field — 15 resolved, 20 open), .ralph/fix_plan.md (M0122-0001 note +
M0119-0006 stale-text correction), .ralph/deferral_ledger.md (new 2026-07-07
M0122-0001 row).

Key symbols: execCreateOpClass/registerOpClassMembers
(internal/executor/operators_ddl.go), RegisterUserOperatorClass/
RegisterAmProcMember/RegisterAmOpMember (internal/catalog/catalog.go),
nextVirtualPgDatabase (internal/executor/operators_storage.go:3585, the
Virtual-UPDATE pattern pg_amproc still lacks).

Findings: CREATE OPERATOR CLASS/FAMILY + pg_amop/pg_amproc member
registration + WAL persistence are FULLY implemented already (landed under
M0119-0004 DU-002, never back-referenced into M0119-0006's fix_plan bullet,
which was stale). `005_opclass_damage.pl` (last open M0119-0006 sub-item)
still blocked on two real, distinct, index-AM-level gaps: (1) pg_amproc has
no Virtual-UPDATE path (only pg_database does); (2) internal/access/btree has
ZERO opclass/comparator-function call sites — the AM never dispatches
through a per-index FUNCTION 1 comparator, so a custom opclass is cataloged
but inert. Neither was implemented this loop (see deferral ledger's 2026-07-07
M0122-0001 row for why: (1) alone flips no test, (2) is architecture-sized).

Next step: continue the M0122-0001 triage — 146/181 unimplemented_feat.json
entries still lack a `status` field (prioritize the remaining
`resolution_check.ledger=open` subset). Do NOT re-investigate the opclass/
pg_amproc question again; it's fully recorded above and in the ledger. If
picking up (1)/(2) above as real implementation work, each needs its own
design doc per the M0119 per-task rule (this M0122-0001 pass was doc-only,
exempt).

Gates run: `make ralph-state-guard` PASS (auto-repaired a stale
running/completed mismatch from the previous loop's clean exit). No Go code
touched this loop (pure JSON/markdown edits) — go build/test not required by
the risk-based gate rules, but `python3 -c "json.load(...)"` confirmed
unimplemented_feat.json stays valid JSON after every edit, and `git diff
--stat` confirms only targeted line insertions (no accidental reformat).

In-flight: none. (A background Explore agent was launched early this loop to
research the same opclass question — if it later returns results, they are
redundant with the manual investigation already recorded here; no action
needed.)
