(idle — nothing in flight)

M0134-0033 (create_procedure.sql) PARKED and committed this loop (a54a4804):
sized at HEAD (131 diff / 2 ^+ERROR / 1 ^-ERROR, 5 root causes), shipped the
smallest CONTAINED bucket (DROP PROCEDURE/FUNCTION not-found no longer
attaches the CALL-only HINT), 131 -> 124 diff lines. Design:
docs/design/m0134-0033-drop-procedure-notfound-hint.md. 4 deferral-ledger
rows appended for the remaining buckets (LINE/^ position-0 sentinel,
pg_get_functiondef raw-text deparse, same-txn DROP visibility, missing
ACL_EXECUTE enforcement — the last is a standalone engine-wide gap worth its
own milestone-sized slice later).

Next loop: per fix_plan.md banner, select M0134-0034 (insert_conflict.sql,
status `failed`) and size it the same way.
