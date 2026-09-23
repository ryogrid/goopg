(idle — nothing in flight)

Last loop (ralph2 #11): sweep item 2 FIXED (40ae756e9): LOCK TABLE 25P01,
DO/CALL bodies not top-level (enterRoutineBody), PL/pgSQL non-DML SQL
passthrough. Sweep task open with 4 items (REINDEX NOTICE, NOTIFY PID 1 +
NOTIFY/LISTEN in routines, DISCARD ALL, CREATE PUBLICATION warning).

OWNER DECISIONS 2026-09-23 appeared in the working tree mid-loop (banner +
new tasks, NOT yet committed by the owner — do not commit or edit the banner;
commit fix_plan only via an index blob built from HEAD + your own edits):
M0145-0001 option (a) — LINEAGE-BASELINE re-pinned; M0145-0029 (one-rel
index-path coverage) and M0145-0030 (behavioural triage) filed and must land
BEFORE the M0145-0008 flip; M0140-0007 → M0146-0002; M0141-S7 held →
M0146-0006; new milestone M0146 gated on the flip.
Next step: re-read the banner — item 3 (M0145) is selectable again; take
M0145-0029 (see docs/design/0100-0149/m0145-0008-flip-test-triage.md group I).
