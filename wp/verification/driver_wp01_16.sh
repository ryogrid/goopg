#!/usr/bin/env bash
# M0120-0002 driver: WP-01..WP-16. Sourced/run after `source wp/verification/run_item.sh`
# and `export RUN=wp/verification/results/<ts>`.
set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$REPO_ROOT/wp/verification/run_item.sh"

echo "=== WP-01: post create ==="
run_item WP-01 post create --post_title='goopg test post' --post_content='body' --post_status=publish --porcelain
postID=$(cat "$RUN/WP-01/stdout.txt" | tr -d '[:space:]')
echo "postID=$postID"
run_item WP-01-confirm post get "$postID" --field=post_status

echo "=== WP-02: post update ==="
run_item WP-02 post update "$postID" --post_title='goopg test post (edited)'
run_item WP-02-confirm post get "$postID" --field=post_title

echo "=== WP-03: post delete (trash) ==="
run_item WP-03 post delete "$postID"
run_item WP-03-confirm post get "$postID" --field=post_status

echo "=== WP-04: post delete --force ==="
run_item WP-04 post delete "$postID" --force
run_item WP-04-confirm post get "$postID"

echo "=== WP-05: post create (page) ==="
run_item WP-05 post create --post_type=page --post_title='goopg page' --post_status=publish --porcelain
pageID=$(cat "$RUN/WP-05/stdout.txt" | tr -d '[:space:]')
echo "pageID=$pageID"
run_item WP-05-confirm post get "$pageID" --field=post_type

echo "=== WP-06: post generate x20 ==="
run_item WP-06-precount post list --format=count
run_item WP-06 post generate --count=20 --post_type=post
run_item WP-06-confirm post list --format=count

echo "=== WP-07: post meta add ==="
run_item WP-07 post meta add "$pageID" gp_key 'gp_value'
run_item WP-07-confirm post meta get "$pageID" gp_key

echo "=== WP-08: post meta update ==="
run_item WP-08 post meta update "$pageID" gp_key 'gp_value2'
run_item WP-08-confirm post meta get "$pageID" gp_key

echo "=== WP-09: post meta delete ==="
run_item WP-09 post meta delete "$pageID" gp_key
run_item WP-09-confirm post meta get "$pageID" gp_key

echo "=== WP-10: term create category ==="
run_item WP-10 term create category 'GoopgCat' --slug=goopgcat --porcelain
termID=$(cat "$RUN/WP-10/stdout.txt" | tr -d '[:space:]')
echo "termID=$termID"
run_item WP-10-confirm term get category "$termID" --field=name

echo "=== WP-11: term create post_tag ==="
run_item WP-11 term create post_tag 'GoopgTag'
run_item WP-11-confirm term list post_tag

echo "=== WP-12: term update category ==="
run_item WP-12 term update category "$termID" --name='GoopgCat2'
run_item WP-12-confirm term get category "$termID" --field=name

echo "=== WP-13: post term set ==="
run_item WP-13 post term set "$pageID" category goopgcat
run_item WP-13-confirm post term list "$pageID" category --field=slug

echo "=== WP-14: term meta add ==="
run_item WP-14 term meta add "$termID" gp_tk 'tv'
run_item WP-14-confirm term meta get "$termID" gp_tk

echo "=== WP-15: term delete category ==="
run_item WP-15 term delete category "$termID"
run_item WP-15-confirm term get category "$termID"

echo "=== WP-16: user create ==="
run_item WP-16 user create bob bob@example.com --role=author --user_pass=secret --porcelain
userID=$(cat "$RUN/WP-16/stdout.txt" | tr -d '[:space:]')
echo "userID=$userID"
run_item WP-16-confirm user get "$userID" --field=user_login

# Persist IDs for later stages (M0120-0003)
{
  echo "postID_deleted=$postID"
  echo "pageID=$pageID"
  echo "termID_deleted=$termID"
  echo "userID=$userID"
} >"$RUN/ids.env"

echo "=== DRIVER DONE ==="
