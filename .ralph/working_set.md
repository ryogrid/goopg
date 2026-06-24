(idle — nothing in flight)

Loop #23: M0118-0008 `reindex-concurrently-toast` PROMOTED to strict (TOAST-exposure
epic slice 5 of 5, design 0118-0088). The FULL M0118-0008 isolation group (25 specs)
now passes strict. REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name> waits on the
synthetic TOAST relation's lockers (DML writes + DROP register a toast-rel lock; a
bare parent LOCK TABLE does not). Committed + pushed.
