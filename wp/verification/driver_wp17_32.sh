#!/usr/bin/env bash
# M0120-0003 driver: WP-17..WP-32. Sourced/run after `source wp/verification/run_item.sh`
# and `export RUN=wp/verification/results/<ts>`.
#
# Reuses IDs from the WP-01..16 run (wp/verification/results/<ts>/ids.env):
#   pageID  (WP-05, still alive after WP-01..16)
#   userID  (WP-16, "bob")
# Pass them in via env before sourcing, e.g.:
#   source wp/verification/results/20260704-072755/ids.env
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/wp/verification/run_item.sh"

: "${pageID:?set pageID from a prior run's ids.env before sourcing this driver}"
: "${userID:?set userID from a prior run's ids.env before sourcing this driver}"

echo "=== WP-17: user update ==="
run_item WP-17 user update "$userID" --display_name='Bob G'
run_item WP-17-confirm user get "$userID" --field=display_name

echo "=== WP-18: user meta update ==="
run_item WP-18 user meta update "$userID" gp_um 'um1'
run_item WP-18-confirm user meta get "$userID" gp_um

echo "=== WP-19: user set-role ==="
run_item WP-19 user set-role "$userID" editor
run_item WP-19-confirm user get "$userID" --field=roles

echo "=== WP-20: user delete --reassign=1 ==="
run_item WP-20 user delete "$userID" --reassign=1
run_item WP-20-confirm user get "$userID"

echo "=== WP-21: comment create ==="
run_item WP-21 comment create --comment_post_ID="$pageID" --comment_content='hi from goopg' --comment_approved=0 --porcelain
commentID=$(cat "$RUN/WP-21/stdout.txt" | tr -d '[:space:]')
echo "commentID=$commentID"
run_item WP-21-confirm comment get "$commentID" --field=comment_content

echo "=== WP-22: comment approve ==="
run_item WP-22 comment approve "$commentID"
run_item WP-22-confirm comment get "$commentID" --field=comment_approved

echo "=== WP-23: comment meta add ==="
run_item WP-23 comment meta add "$commentID" gp_cm 'cm1'
run_item WP-23-confirm comment meta get "$commentID" gp_cm

echo "=== WP-24: comment delete --force ==="
run_item WP-24 comment delete "$commentID" --force
run_item WP-24-confirm comment get "$commentID"

echo "=== WP-25: option add ==="
run_item WP-25 option add gp_opt 'gp_optval'
run_item WP-25-confirm option get gp_opt

echo "=== WP-26: option update (blogname) ==="
run_item WP-26 option update blogname 'goopg blog'
run_item WP-26-confirm option get blogname

echo "=== WP-27: option delete ==="
run_item WP-27 option delete gp_opt
run_item WP-27-confirm option get gp_opt

echo "=== WP-28: option update (TOAST-sized ~20000 bytes) ==="
gp_big_val="$(head -c 20000 /dev/zero | tr '\0' x)"
run_item WP-28 option update gp_big "$gp_big_val"
run_item WP-28-confirm option get gp_big

echo "=== WP-29: transient set ==="
run_item WP-29 transient set gp_tr 'trval' 3600
run_item WP-29-confirm transient get gp_tr

echo "=== WP-30: plugin activate hello ==="
run_item WP-30 plugin activate hello
run_item WP-30-confirm plugin list --status=active --field=name

echo "=== WP-31: plugin deactivate hello ==="
run_item WP-31 plugin deactivate hello
run_item WP-31-confirm plugin list --status=active --field=name

echo "=== WP-32: raw SQL via wp db query ==="
run_item WP-32-insert db query "INSERT INTO wp_options (option_name,option_value,autoload) VALUES ('gp_raw','rawval','no')"
run_item WP-32-insert-confirm option get gp_raw
run_item WP-32-update db query "UPDATE wp_options SET option_value='rawval2' WHERE option_name='gp_raw'"
run_item WP-32-update-confirm option get gp_raw
run_item WP-32-delete db query "DELETE FROM wp_options WHERE option_name='gp_raw'"
run_item WP-32-delete-confirm option get gp_raw

# Persist IDs for later stages (M0120-0004/0005)
{
  echo "userID_deleted=$userID"
  echo "commentID_deleted=$commentID"
  echo "pageID=$pageID"
} >"$RUN/ids.env"

echo "=== DRIVER DONE ==="
