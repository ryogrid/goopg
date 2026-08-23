(idle — nothing in flight)

Task just completed: M0134-0086 (with.sql). Landed a real OOM safety-net fix
(maxRecursiveIterationRows guard in internal/executor/operators_recursive_cte.go)
after sizing found a live host-endangering RSS blow-up (22+ GB) in a nested
mutually-recursive WITH RECURSIVE query. Committed as 8de8ab80. Design doc
docs/design/m0134-0086-recursive-cte-iteration-oom-guard.md, ledger row
appended 2026-08-24, fix_plan.md M0134-0086 marked PARKED.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0087 (xid.sql, status
`failed`)**. Size it live against ./postgres oracle via
scripts/pg-regress-runner.sh --verbose xid (run in background with a
generous timeout — setup alone takes ~2-3 min). CAUTION carried forward:
this loop found that some regress cases can drive goopg RSS to 20+ GB in
under 2 minutes (host has only ~31GB RAM + swap) — watch `ps -o rss` on the
goopg PID while any regress file runs, and kill -KILL (never bare pkill -f)
promptly if RSS climbs unbounded before deciding whether it's a similar
recursion/OOM bug worth fixing first.
