#!/bin/sh
#
# seed.sh — run inside the wp-cli container. Installs WordPress
# non-interactively in English (if not already installed) and seeds a small
# set of sample blog posts (once). Idempotent.
#
# Expects env: WP_URL, WP_TITLE, WP_ADMIN_PASSWORD
set -eu

# ---- install (English) ------------------------------------------------------
if wp core is-installed >/dev/null 2>&1; then
    echo "WordPress already installed; skipping core install."
else
    echo "Installing WordPress at $WP_URL ..."
    wp core install \
        --url="$WP_URL" \
        --title="$WP_TITLE" \
        --admin_user="admin" \
        --admin_password="$WP_ADMIN_PASSWORD" \
        --admin_email="admin@example.com" \
        --skip-email
fi

# Site language: English (en_US is built in). WPLANG empty == en_US.
wp option update WPLANG "" >/dev/null 2>&1 || true
wp option update blogdescription "A WordPress blog powered by goopg — PostgreSQL, reimplemented in Go" >/dev/null 2>&1 || true

# ---- seed sample posts (once) ----------------------------------------------
SENTINEL="Running WordPress on goopg"
if wp post list --post_type=post --field=post_title --format=csv 2>/dev/null | grep -qF "$SENTINEL"; then
    echo "Sample posts already present; skipping seed."
    exit 0
fi

echo "Seeding sample blog posts ..."

# A category for the sample content.
CAT_ID="$(wp term create category "goopg" --slug=goopg --porcelain 2>/dev/null || wp term list category --slug=goopg --field=term_id 2>/dev/null | head -n1)"
[ -n "${CAT_ID:-}" ] || CAT_ID=1

# Refresh the default "Hello world!" post into a proper welcome article.
wp post update 1 \
    --post_title="Welcome to the goopg WordPress Blog" \
    --post_content="This blog runs on <strong>WordPress</strong> with its entire database served by <strong>goopg</strong>, a from-scratch reimplementation of PostgreSQL written in Go. Browse the sample posts below to see a real WordPress workload — posts, categories, comments and options — stored and queried through goopg over the PostgreSQL wire protocol." \
    >/dev/null 2>&1 || true

create_post() {
    title="$1"; content="$2"
    wp post create \
        --post_status=publish \
        --post_type=post \
        --post_author=1 \
        --post_category="$CAT_ID" \
        --post_title="$title" \
        --post_content="$content" \
        >/dev/null
    echo "  + $title"
}

create_post "Running WordPress on goopg" \
"<p>WordPress was built for MySQL, yet here it is running on <em>goopg</em> — a PostgreSQL-compatible database engine implemented in Go. The bridge is <a href=\"https://github.com/PostgreSQL-For-Wordpress/postgresql-for-wordpress\">PG4WP</a>, a drop-in <code>wp-content/db.php</code> that translates WordPress's MySQL calls into PostgreSQL queries on the fly.</p><p>Every page you load issues real SQL against goopg: reading options, fetching posts, resolving terms and rendering comments.</p>"

create_post "How the pieces fit together" \
"<p>The stack has three layers:</p><ol><li><strong>WordPress</strong> (PHP + Apache) running in a Docker container.</li><li><strong>PG4WP</strong>, the PostgreSQL connector that reimplements WordPress's <code>mysqli_*</code> functions on top of the PHP <code>pgsql</code> extension.</li><li><strong>goopg</strong>, the PostgreSQL-compatible server, running on the host and speaking the PostgreSQL v3 wire protocol.</li></ol><p>To WordPress it looks like an ordinary database; to goopg it looks like an ordinary PostgreSQL client.</p>"

create_post "Why PostgreSQL compatibility matters" \
"<p>goopg's goal is faithful, byte-for-byte compatibility with vanilla PostgreSQL 18 — the same wire protocol, catalog layout, SQL semantics and error codes. Driving it with a demanding real-world application like WordPress is a great way to exercise that compatibility across DDL, transactions, indexes and a wide surface of SQL.</p>"

create_post "Sample data for the blog" \
"<p>This post is part of a small set of seeded articles so the blog isn't empty on first launch. They were created with <code>wp-cli</code> — which itself talks to goopg through PG4WP — proving the write path end to end: <code>INSERT</code>s into <code>wp_posts</code>, term relationships, and post meta all land in goopg.</p>"

create_post "Try it yourself" \
"<p>Open <code>wp-admin</code> and create a post, edit a page, or leave a comment. Then connect to goopg directly with <code>psql</code> and watch your changes appear in the <code>wp_posts</code>, <code>wp_options</code> and <code>wp_comments</code> tables. It's WordPress all the way down — just with PostgreSQL underneath.</p>"

echo "Seeding complete."
