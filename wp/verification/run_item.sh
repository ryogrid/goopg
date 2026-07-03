#!/usr/bin/env bash
# WordPress-on-goopg verification harness (M0120-0001).
#
# Runs one WP-CLI checklist item and captures the three evidence streams
# FLOW.md §1-3 require: WP-CLI stdout/stderr/exit, the goopg statement-log
# slice for exactly this item's window (byte-offset into wp/goopg-wp.log,
# since goopg emits to one shared log), and the PG4WP rewrite/SQL log from
# inside the wpcli container. Confirming reads are just another run_item call
# (or a plain WP invocation) whose output the caller inspects for the
# checklist's PASS criterion.
#
# Usage (from the repo root, after `source wp/verification/run_item.sh`):
#   RUN=wp/verification/results/$(date +%Y%m%d-%H%M%S)
#   mkdir -p "$RUN"
#   run_item WP-01 post create --post_title='goopg test post' --post_status=publish --porcelain
#   postID=$(cat "$RUN/WP-01/stdout.txt")
#   run_item WP-01-confirm post get "$postID" --field=post_status
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GLOG="${GLOG:-$REPO_ROOT/wp/goopg-wp.log}"
COMPOSE="${COMPOSE:-docker compose -f $REPO_ROOT/wp/docker-compose.yml}"

WP() { $COMPOSE run --rm --no-TTY wpcli wp "$@"; }

run_item() {
  local id="$1"; shift
  : "${RUN:?set RUN=wp/verification/results/<timestamp> before calling run_item}"
  local d="$RUN/$id"
  mkdir -p "$d"

  local goff
  goff=$(wc -c <"$GLOG" 2>/dev/null || echo 0)

  WP "$@" >"$d/stdout.txt" 2>"$d/stderr.txt"
  echo "$?" >"$d/exit.txt"
  printf '%s\n' "$*" >"$d/command.txt"

  tail -c +"$((goff + 1))" "$GLOG" 2>/dev/null | grep 'msg=statement' >"$d/goopg_statements.log" || true

  $COMPOSE run --rm --no-TTY --entrypoint sh wpcli \
    -c 'cat wp-content/pg4wp/logs/pg4wp_*.log 2>/dev/null' >"$d/pg4wp.log" 2>/dev/null || true
}

baseline_snapshot() {
  local out="${1:?usage: baseline_snapshot <output-file>}"
  {
    echo "post_count=$(WP post list --format=count 2>/dev/null)"
    echo "user_count=$(WP user list --format=count 2>/dev/null)"
    echo "comment_count=$(WP comment list --format=count 2>/dev/null)"
  } >"$out"
}
