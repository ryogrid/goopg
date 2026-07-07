Task: M-NIGHTLY triage (re-confirmed no new work — run 20260707-000712 is
still the same run, all 8 AI-items already have checked fix_plan tasks, gates
green). Actual loop work: M0122-0001 backlog triage continuation, doc-only
(exempt from design-doc rule).

Files: unimplemented_feat.json (surgical text-level Edits only — located each
entry's exact line range via its "feature" line as a unique anchor, inserted
a `status` line after `resume_point`, and for 13 entries rewrote a stale
`code_audit` string to cite current evidence). No other files touched.

Key symbols: none new this loop (pure backlog bookkeeping, no code read
beyond what 6 parallel Explore/general-purpose agents already cited per
entry — see the JSON's updated `code_audit` fields for exact file:line
evidence per item).

Findings: triaged the full 49-entry `resolution_check.ledger == "open" AND
status missing` subset (the highest-confidence subset, since each already
has a matching deferral_ledger.md row). Verdict: 15 resolved, 34 open.
13 of the 15 "resolved" entries had a STALE `code_audit` field claiming
"confirmed-open" from an earlier automated pass — corrected those in place
(RESOLVED <date>: ... citing current file:line + landing commit/test).
The 15 resolved-but-mislabeled entries: pg_index persistent heap table
(M0113), regexp_matches real SRF (not NULL stub), CREATE/DROP DATABASE,
pg_basebackup WAL-stream-complete, pg_stat_io real counters, CHECK OPTION
enforcement, HANDLER/VALIDATOR FDW resolution, char(18) vs bpchar
disambiguation, datconnlimit invalid-DB filtering, race-gate re-enable +
TestMultiWriterStress flake fix, TPC-H Q8 fix, and the M0107-002 Datum 48B
flip. NOTE pg_get_serial_sequence (IDX 100) is still genuinely OPEN — it
was investigated in the same batch but did NOT flip; don't confuse it with
the resolved regexp_matches entry next to it in the file. Confirmed still
genuinely OPEN and unchanged from M0122-0001's first pass: 005_opclass_damage
(both sub-entries, IDX 142/143) — pg_amproc has no Virtual-UPDATE path and
internal/access/btree has zero opclass-comparator dispatch call sites.

Next step: continue M0122-0001 — 97 unimplemented_feat.json entries still
lack a `status` field, and the `ledger=="open"` high-confidence subset is now
FULLY EXHAUSTED (0 remaining). The next batch must use `resolution_check.
ledger == "no-match"` entries instead (no matching deferral_ledger.md row to
anchor against — these need pure code-audit investigation, same
parallel-agent-batch method as this loop, just without a ledger citation to
start from). Query: `python3 -c "import json; d=json.load(open(
'unimplemented_feat.json'))['unimplemented_features']; print(len([f for f in
d if 'status' not in f]))"` → 97.

Gates run: `make ralph-state-guard` PASS (auto-repaired the same transient
running/completed mismatch seen last loop — the mismatch reproduces every
loop at this checkpoint and is a known benign artifact, not a regression).
No Go code touched (pure JSON edit) — go build/test not required by the
risk-based gate rules. `python3 -c "json.load(...)"` confirmed valid JSON
after every edit stage (including the em-dash/arrow unicode-escape fix
below). `git diff --stat` confirms only targeted +1-line insertions plus 13
single-line code_audit replacements — no accidental reformat of the other
132 untouched entries.

In-flight: none. One self-caught mistake worth recording: my first pass used
Python's `json.dumps()` to build the replacement `code_audit` strings, which
defaults to `ensure_ascii=True` and silently converted literal em-dashes (—)
and arrows (→) to `—`/`→` escapes — inconsistent with this file's
established "literal UTF-8, no \u escapes" convention (confirmed 33 literal
em-dashes and 5 literal arrows already exist elsewhere in the file). Caught
via `grep -c '\\\\u2014'` before committing and fixed with a plain string
`.replace()` pass. Next loop: if generating new code_audit/evidence text
programmatically, build the JSON string manually or use
`json.dumps(s, ensure_ascii=False)` — never bare `json.dumps(s)`.
