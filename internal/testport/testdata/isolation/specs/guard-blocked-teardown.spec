# The setup/teardown hang guard (M-NIGHTLY AI-20261022-011400-001): the test
# holds an uncommitted UPDATE of guard_blocked's only row from its own
# connection, so this spec's global teardown, which updates the same row,
# waits for that transaction. The runner must give up at its guard and fail
# the spec with the server's state, not wait forever. There is no expected
# output: the spec is never meant to complete.

setup
{
  CREATE TABLE IF NOT EXISTS guard_side (i int);
}

teardown
{
  UPDATE guard_blocked SET i = 3 WHERE i = 1;
}

session s1
step s1a	{ SELECT 1 AS one; }

permutation s1a
