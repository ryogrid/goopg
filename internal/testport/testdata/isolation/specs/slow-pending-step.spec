# A step that is still waiting when the permutation's steps run out, and that
# completes only after the runner's former 5 s drain window (M-NIGHTLY
# AI-20261020-013101-005): s2u waits on s1's row lock and gives up at its
# 6 s lock_timeout. isolationtester waits for it (max_step_wait, 360 s), so
# the expected output reports the step completed with its error.
# Expected output captured from PG 18.3's isolationtester.

setup
{
  CREATE TABLE slow_pending (id int PRIMARY KEY, v int);
  INSERT INTO slow_pending VALUES (1, 0);
}

teardown
{
  DROP TABLE slow_pending;
}

session s1
step s1b	{ BEGIN; }
step s1u	{ UPDATE slow_pending SET v = 1 WHERE id = 1; }
teardown	{ ROLLBACK; }

session s2
setup		{ SET lock_timeout = '6s'; }
step s2u	{ UPDATE slow_pending SET v = 2 WHERE id = 1; }

permutation s1b s1u s2u
