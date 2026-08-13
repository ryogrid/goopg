# M0132-S8 — a block opened by one session is invisible to a second until
# COMMIT, and rolled back on ROLLBACK.
#
# The mixed driver shape (BEGIN via the simple protocol, parameterised DML via
# the extended protocol, COMMIT/ROLLBACK via the simple protocol) forms ONE block
# on the connection. This spec pins the SEMANTIC contract of that block at the
# concurrency level: the uncommitted INSERT is invisible to a second session, and
# a ROLLBACK (rather than COMMIT) makes it vanish. The protocol interleaving
# itself is pinned by the server-level tests (internal/server/extended_txn_mixed_test.go)
# and the lib/pq driver test, because the isolation runner faithfully reproduces
# upstream isolationtester's PQexec semantics — argument-less queries, i.e. the
# simple protocol — and the upstream spec format has no parameterised steps.

setup { CREATE TABLE mixed_blk (id int PRIMARY KEY); }
teardown { DROP TABLE mixed_blk; }

session "s1"
step "s1_begin"    { BEGIN; }
step "s1_insert"   { INSERT INTO mixed_blk VALUES (1); }
step "s1_commit"   { COMMIT; }
step "s1_rollback" { ROLLBACK; }

session "s2"
step "s2_read" { SELECT id FROM mixed_blk; }

permutation "s1_begin" "s1_insert" "s2_read" "s1_commit" "s2_read"
permutation "s1_begin" "s1_insert" "s2_read" "s1_rollback" "s2_read"
