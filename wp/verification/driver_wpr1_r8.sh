#!/usr/bin/env bash
# M0120-0004 driver: WP-R1..WP-R8 (read-only). Sourced/run after
# `source wp/verification/run_item.sh` and `export RUN=wp/verification/results/<ts>`.
#
# Reuses IDs from a prior WP-01..32 run (wp/verification/results/<ts>/ids.env):
#   pageID  (WP-05, still alive)
# userID and commentID from that run were deleted by WP-20/WP-24 respectively,
# so this driver does NOT reuse them — WP-R2 reads the built-in WP post ID 1
# (the "Hello world!" welcome post) instead of a userID/commentID.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/wp/verification/run_item.sh"

: "${pageID:?set pageID from a prior runs ids.env before sourcing this driver}"

echo "=== WP-R1: post list --format=count ==="
run_item WP-R1 post list --format=count

echo "=== WP-R2: post get 1 --field=post_title ==="
run_item WP-R2 post get 1 --field=post_title

echo "=== WP-R3: user list --format=table ==="
run_item WP-R3 user list --format=table

echo "=== WP-R4: term list category --format=count ==="
run_item WP-R4 term list category --format=count

echo "=== WP-R5: comment list --format=count ==="
run_item WP-R5 comment list --format=count

echo "=== WP-R6: option get siteurl ==="
run_item WP-R6 option get siteurl

echo "=== WP-R7: db query SELECT COUNT(*) (expected pg4wp/harness limitation, see WP-32) ==="
run_item WP-R7 db query "SELECT COUNT(*) FROM wp_posts WHERE post_status='publish'"

echo "=== WP-R8: core version / db size --tables ==="
run_item WP-R8-version core version
run_item WP-R8-size db size --tables

echo "=== DRIVER DONE ==="
