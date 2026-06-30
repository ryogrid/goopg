(idle — nothing in flight)

Loop #90 COMPLETE: M0119-0004 DU-002 slice 359 — conditional CREATE RULE
(`WHERE (qual) DO INSTEAD NOTHING`) now round-trips through real pg_dump 18.3.
parser CreateRuleStmt.Qual + catalog RuleInfo.Qual + execCreateRule deparse via
defaultExprToSQL + buildRuleDefString WHERE-on-own-line. Byte-identical golden
captured from real PG (/tmp/du359_ref). Committed.

Next loop: pick a fresh M0119-0004 pg_dump slice or another M0119 item. Open
under M0119-0004: action-command CREATE RULE (`DO INSTEAD INSERT/UPDATE/…` —
needs a full query reverse-compiler, milestone-sized, ledgered loop #90);
reserved-keyword-named-role quoting (claimed working — guard slice if a real
gap); extended-protocol commit-time deferral (architecturally entangled).
Heavier M0119 items: M0119-0002 (CLOG store swap, full-gate session),
M0119-0005/0006 server tiers (need index AMs / verify_heapam), M0119-0007
(logical decoding — not actionable).
