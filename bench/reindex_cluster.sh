#!/usr/bin/env bash
# Re-build every user B-tree index on a long-lived goopg bench cluster.
#
# Why this exists (M-NIGHTLY AI-20260811-014635-012): the M0130-S11 nbtree
# series changed the on-disk index format three times (S11.2b page shape,
# S11.3 metapage, S11.4 tuple format), each marked "REINDEX REQUIRED". Nothing
# re-runs that REINDEX on the bench clusters, which are loaded once and kept
# for months, so an index built before a flip is read by the post-flip reader
# and every scan over it dies with
#
#     ERROR: btree: index contains corrupted page at block 0: special size N, want 16
#
# That is not corruption — it is a stale-format index. The SF=0.5 TPC-DS
# cluster was remediated by hand when S11.4 landed; the SF=1 cluster was not,
# and the nightly clones SF=1, so 25 TPC-DS queries turned ERROR in run
# 20260811-014635. Run this after any REINDEX-required change against every
# cluster in the port table in CLAUDE.md.
#
# Usage: bench/reindex_cluster.sh <port> [db]
set -euo pipefail

PORT="${1:?usage: reindex_cluster.sh <port> [db]}"
DB="${2:-postgres}"
HOST=127.0.0.1
USER_NAME="${PGUSER:-postgres}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PSQL="$REPO_ROOT/postgres/local_install/bin/psql"
[ -x "$PSQL" ] || PSQL=psql

q() { "$PSQL" -h "$HOST" -p "$PORT" -U "$USER_NAME" -d "$DB" -Atq "$@"; }

mapfile -t IDX < <(q -c "SELECT indexname FROM pg_indexes WHERE schemaname='public' ORDER BY 1")
if [ "${#IDX[@]}" -eq 0 ]; then
	echo "reindex_cluster: no public indexes on $HOST:$PORT/$DB — nothing to do"
	exit 0
fi

echo "reindex_cluster: $HOST:$PORT/$DB — ${#IDX[@]} indexes"
fail=0
for ix in "${IDX[@]}"; do
	start=$(date +%s)
	if q -v ON_ERROR_STOP=1 -c "REINDEX INDEX public.\"$ix\"" >/dev/null; then
		echo "  ok   $ix ($(( $(date +%s) - start ))s)"
	else
		echo "  FAIL $ix"
		fail=$((fail + 1))
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "reindex_cluster: $fail index(es) FAILED"
	exit 1
fi
echo "reindex_cluster: all ${#IDX[@]} indexes rebuilt"
