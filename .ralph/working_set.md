(idle — nothing in flight)

Last landed (loop #4): `stats` PROMOTED to pass-required (design 0118-0133) —
the FINAL rung of M0118-0009 stats. Isolation-runner per-session connection
reuse: RunSpec opens 1 persistent conn/session ONCE per spec (sessionConns +
openSessionConns), reused across EVERY permutation (mirrors isolationtester.c
main()); runPermutation re-runs only session `setup` SQL per permutation so a
step-set GUC (SET track_functions='all') persists forward → fixed L3732.
TestPort_IsolationStats now runIsoSpecStrict — PASS byte-for-byte.

`stats` was the LAST failed M0118-0009 spec → M0118-0009 group resolved.

Pre-existing (NOT this change, confirmed identical at HEAD): vacuum-skip-locked
+ vacuum-concurrent-drop fail (goopg VACUUM SKIP_LOCKED doesn't emit the
"skipping vacuum of X --- lock not available" WARNING) = the "4 failed" of the
117/4 isolation tally. Candidate next task if a non-stats isolation item is
wanted.
