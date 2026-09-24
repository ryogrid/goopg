(idle — nothing in flight)

Loop #48 landed an M0122-0008 slice: the ssl* GUC family as the no-SSL
oracle build defines it (`0853825e4`). It filed three children under
M0122-0008:
- TLS/channel binding: [!], an owner call;
- ALTER SYSTEM is a no-op: [ ], selectable;
- GUC range-error wording: [ ], selectable.
Next per banner: those two M0122-0008 children, then the rest of M0122.
M0119 and the M-NIGHTLY work_mem item remain owner-blocked.
