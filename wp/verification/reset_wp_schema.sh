#!/usr/bin/env bash
#
# reset_wp_schema.sh — recreate the WordPress-on-goopg test schema from scratch.
#
# WHY THIS EXISTS
# --------------
# The M0120 WordPress WP-CLI verification harness stores its tables in the goopg
# server on 127.0.0.1:5544 (db `postgres`). That schema can go *stale*: tables
# created by an older goopg build may be missing the sequence-backed PK defaults
# / primary keys / indexes that current DDL produces (root-caused 2026-07-03 —
# every `wp_*` PK column, e.g. `wp_posts.ID`, had no default and no index, so a
# plain `wp post create` failed with `null value in column "ID" ... not-null`).
# The only fix is to DROP the core tables and let PG4WP's `dbDelta` recreate them
# against the current binary, then re-seed the baseline content.
#
# This wraps the exact reset sequence in ONE named, reviewed, idempotent command
# so the headless Ralph loop can run a single scoped invocation instead of
# emitting a raw `DROP TABLE ... CASCADE`, which auto-mode's destructive-command
# classifier (correctly) refuses when it appears unscoped. To let the loop run
# it without a prompt, add an allow rule for this script to
# .claude/settings.local.json (requires the user to authorize widening the
# permission allow-list). Everything it touches is 100% synthetic WordPress
# seed/test content in the dedicated :5544 verification instance — NOT user data
# and NOT any other goopg data directory.
#
# USAGE (from the repo root):
#   wp/verification/reset_wp_schema.sh
#
# After it completes, re-run the verification driver as usual, e.g.:
#   source wp/verification/run_item.sh
#   export RUN=wp/verification/results/$(date +%Y%m%d-%H%M%S); mkdir -p "$RUN"
#   bash wp/verification/driver_wp01_16.sh
set -euo pipefail

WP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$WP_DIR/.." && pwd)"
COMPOSE="docker compose -f $WP_DIR/docker-compose.yml"

# --- fixed target: the dedicated WP verification instance -------------------
# Hardcoded on purpose so this destructive reset can only ever hit the synthetic
# :5544 test schema, never another port / data directory.
PGPORT_WP="5544"
PGUSER_WP="postgres"
PGDB_WP="postgres"
PG_BIN="$REPO_ROOT/postgres/local_install/bin"
PG_LIB="$REPO_ROOT/postgres/local_install/lib"

WP_URL="http://localhost:8080"
WP_TITLE="goopg WordPress Blog"
PW_FILE="$WP_DIR/.wp-admin-password"

# The 12 WordPress core tables (default `wp_` prefix). CASCADE also removes the
# dependent objects (FKs / views) so dbDelta can recreate a clean schema.
WP_TABLES="wp_commentmeta, wp_comments, wp_links, wp_options, wp_postmeta, \
wp_posts, wp_term_relationships, wp_term_taxonomy, wp_termmeta, wp_terms, \
wp_usermeta, wp_users"

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

psql_wp() {
    LD_LIBRARY_PATH="$PG_LIB" "$PG_BIN/psql" \
        -h 127.0.0.1 -p "$PGPORT_WP" -U "$PGUSER_WP" -d "$PGDB_WP" "$@"
}

# --- 0. preconditions --------------------------------------------------------
[ -x "$PG_BIN/psql" ] || die "psql not found at $PG_BIN/psql (build the PG oracle first)"
psql_wp -tAc 'select 1' >/dev/null 2>&1 \
    || die "goopg WP server not reachable on 127.0.0.1:$PGPORT_WP (is goopg-wp.scope active?)"
[ -f "$PW_FILE" ] || die "admin password file missing: $PW_FILE (run wp/setup.sh once)"
WP_ADMIN_PASSWORD="$(cat "$PW_FILE")"

# --- 1. drop the stale core tables ------------------------------------------
log "Dropping WordPress core tables on :$PGPORT_WP (synthetic test schema)"
psql_wp -v ON_ERROR_STOP=1 -c "DROP TABLE IF EXISTS ${WP_TABLES} CASCADE;"

# --- 2. reinstall + reseed via PG4WP (dbDelta recreates a fresh schema) ------
# seed.sh runs `wp core install` when the tables are gone, then seeds the
# 7-post / 1-user / 1-comment baseline. Same invocation as wp/setup.sh step 5.
log "Reinstalling WordPress + reseeding baseline via wp-cli (dbDelta recreates schema)"
$COMPOSE run --rm \
    -e WP_URL="$WP_URL" \
    -e WP_TITLE="$WP_TITLE" \
    -e WP_ADMIN_PASSWORD="$WP_ADMIN_PASSWORD" \
    --entrypoint sh wpcli /seed/seed.sh

# --- 3. verify the recreated schema is healthy ------------------------------
log "Verifying recreated schema (wp_posts must have a PK/index + ID default)"
idx_count="$(psql_wp -tAc \
    "SELECT count(*) FROM pg_indexes WHERE tablename='wp_posts'" 2>/dev/null | tr -d '[:space:]')"
id_default="$(psql_wp -tAc \
    "SELECT pg_get_expr(adbin, adrelid) FROM pg_attrdef d \
       JOIN pg_attribute a ON a.attrelid=d.adrelid AND a.attnum=d.adnum \
      WHERE d.adrelid='wp_posts'::regclass AND a.attname='ID'" 2>/dev/null | tr -d '[:space:]')"
echo "  wp_posts index count = ${idx_count:-0}"
echo "  wp_posts.ID default   = ${id_default:-<none>}"
if [ "${idx_count:-0}" -lt 1 ] || [ -z "${id_default:-}" ]; then
    die "schema still unhealthy after reset (idx=$idx_count default='$id_default') — investigate DDL/serial path"
fi

log "WP schema reset complete — driver_wp01_16.sh can now be re-run."
