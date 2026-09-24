(idle — nothing in flight)

This loop: M0145-0008f landed as 11968441a (executor only). The scan's
retention clone copies only the deformed window, and the hashed grouping loop
uses scratch key buffers. Q18 went from 9.53 s to 8.48 s; serial GROUP BY
from 4.55 s to 3.8 s (PG 1.86 s). The remaining gap is the scan's per-row row
allocation, filed as M0145-0008k (recon), which also covers count(*) being
slower than sum().
Note: the RALPH_LOOP guard pattern-matches command TEXT. A heredoc mentioning
the reference port next to words like l_comment or ANALYZE is blocked; keep
the port number out of write commands.

Next per the banner (item 3): M0145-0008g (expression IN-list selectivity,
Q22), then 0008h (planner crash, S2 escalation, still awaiting owner
placement), 0008i, 0008j, 0008k, then the legacy-deletion slices and the
M0146 continuation.
Parked: tmp/m0122-alter-system-wip.patch.
