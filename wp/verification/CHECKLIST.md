# WordPress-on-goopg — WP-CLI Verification Checklist

This checklist enumerates **40** WordPress operations, exercised through
**WP-CLI**, used to verify that WordPress behaves correctly when backed by
**goopg** (via the PG4WP MySQL→PostgreSQL translation layer). **32** items are
database **write** operations (`W`, WP-01…WP-32) and **8** are read-only
(`R`, WP-R1…WP-R8).

Execution, log capture, and evidence storage are defined in
[`FLOW.md`](./FLOW.md). The results of a run belong under
`wp/verification/results/<timestamp>/`. Running these items and capturing the
evidence is milestone **M0120**; fixing any failures is **M0121**.

## Conventions

All commands run in the on-demand `wpcli` container (shares the WordPress
`wp_html` volume and PG4WP `db.php`). Define once per shell:

```bash
# from the repo root; loads wp/.env for GOOPG_DB_HOST
WP() { docker compose -f wp/docker-compose.yml run --rm --no-TTY wpcli wp "$@"; }
```

Then every `wp …` below means `WP …`. IDs written by a create step
(`<postID>`, `<pageID>`, `<termID>`, `<userID>`, `<commentID>`) are captured
from its `--porcelain` output and reused by later steps. Tables use the default
`wp_` prefix. A `W` item's **PASS** criterion always includes a confirming read
(a `wp … get`/`list` or `wp db query 'SELECT …'`) proving the row actually
changed — never just a zero exit code.

### Known non-goopg limitation (do not count as a goopg FAIL)

Update checks are disabled by `mu-plugins/disable-update-checks.php` because
`wp_version_check()` probes MySQL `information_schema.TABLES`, which PG4WP's
`SelectSQLRewriter` cannot translate. Items that would trigger that path (e.g.
`wp core check-update`, `wp plugin list --update=available`) are **excluded**.
A failure rooted in the PG4WP rewriter rather than in goopg's SQL execution is
recorded as a **PG4WP limitation**, not a goopg bug.

**Second known non-goopg limitation** (found in M0120-0003, item WP-32):
`wp db query "..."` does not go through WordPress's PHP `$wpdb`/PG4WP layer at
all — WP-CLI shells out to the native `mysql` CLI client, connecting directly
to `DB_HOST` (goopg's PostgreSQL wire-protocol listener). The `mysql` client's
handshake fails immediately (`ERROR 2013 ... Lost connection ... reading
initial communication packet`) because it expects a MySQL greeting packet, not
a PostgreSQL one — confirmed via the goopg statement log showing the query
never arrives at goopg at all. This affects **every** `wp db query` invocation
(WP-32 and the read-only **WP-R7**), and is unfixable without goopg speaking a
second, MySQL-compatible wire protocol (out of scope) or a PG4WP-side shim for
WP-CLI's `db` command family (also out of scope). Classify as **harness/PG4WP
limitation**, never as a goopg bug — see
`wp/verification/results/20260704-073700/summary.md` for the full repro.

---

## A. Posts & pages (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-01 | W | `post create --post_title='goopg test post' --post_content='body' --post_status=publish --porcelain` | Prints new `<postID>`; `post get <postID> --field=post_status` = `publish` | `INSERT wp_posts` |
| WP-02 | W | `post update <postID> --post_title='goopg test post (edited)'` | `Success`; `post get <postID> --field=post_title` shows new title | `UPDATE wp_posts` |
| WP-03 | W | `post delete <postID>` | Trashed; `post get <postID> --field=post_status` = `trash` | `UPDATE wp_posts SET post_status='trash'` |
| WP-04 | W | `post delete <postID> --force` | Deleted; `post get <postID>` errors "Invalid post ID" | `DELETE wp_posts`, `DELETE wp_postmeta` |
| WP-05 | W | `post create --post_type=page --post_title='goopg page' --post_status=publish --porcelain` | Prints `<pageID>`; `post get <pageID> --field=post_type` = `page` | `INSERT wp_posts` |
| WP-06 | W | `post generate --count=20 --post_type=post` | Creates 20 posts; `post list --format=count` rises by 20 | bulk `INSERT wp_posts` |

## B. Post meta (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-07 | W | `post meta add <pageID> gp_key 'gp_value'` | `Success`; `post meta get <pageID> gp_key` = `gp_value` | `INSERT wp_postmeta` |
| WP-08 | W | `post meta update <pageID> gp_key 'gp_value2'` | `Success`; `post meta get <pageID> gp_key` = `gp_value2` | `UPDATE wp_postmeta` |
| WP-09 | W | `post meta delete <pageID> gp_key` | `Success`; `post meta get <pageID> gp_key` empty/errors | `DELETE wp_postmeta` |

## C. Taxonomy: categories, tags, terms (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-10 | W | `term create category 'GoopgCat' --slug=goopgcat --porcelain` | Prints `<termID>`; `term get category <termID> --field=name` = `GoopgCat` | `INSERT wp_terms`+`wp_term_taxonomy` |
| WP-11 | W | `term create post_tag 'GoopgTag'` | `Success`; appears in `term list post_tag` | `INSERT wp_terms`+`wp_term_taxonomy` |
| WP-12 | W | `term update category <termID> --name='GoopgCat2'` | `Success`; `term get category <termID> --field=name` = `GoopgCat2` | `UPDATE wp_terms` |
| WP-13 | W | `post term set <pageID> category goopgcat` | `Success`; `post term list <pageID> category --field=slug` includes `goopgcat`; taxonomy `count` incremented | `INSERT wp_term_relationships`, `UPDATE wp_term_taxonomy.count` |
| WP-14 | W | `term meta add <termID> gp_tk 'tv'` | `Success`; `term meta get <termID> gp_tk` = `tv` | `INSERT wp_termmeta` |
| WP-15 | W | `term delete category <termID>` | `Success`; `term get category <termID>` errors | `DELETE wp_terms`+`wp_term_taxonomy`+`wp_term_relationships` |

## D. Users & roles (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-16 | W | `user create bob bob@example.com --role=author --user_pass=secret --porcelain` | Prints `<userID>`; `user get <userID> --field=user_login` = `bob` | `INSERT wp_users`+`wp_usermeta` |
| WP-17 | W | `user update <userID> --display_name='Bob G'` | `Success`; `user get <userID> --field=display_name` = `Bob G` | `UPDATE wp_users` |
| WP-18 | W | `user meta update <userID> gp_um 'um1'` | `Success`; `user meta get <userID> gp_um` = `um1` | `INSERT`/`UPDATE wp_usermeta` |
| WP-19 | W | `user set-role <userID> editor` | `Success`; `user get <userID> --field=roles` = `editor` | `UPDATE wp_usermeta (wp_capabilities)` |
| WP-20 | W | `user delete <userID> --reassign=1` | `Success`; `user get <userID>` errors; reassigned posts now owned by user 1 | `DELETE wp_users`+`wp_usermeta`, `UPDATE wp_posts.post_author` |

## E. Comments (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-21 | W | `comment create --comment_post_ID=<pageID> --comment_content='hi from goopg' --comment_approved=0 --porcelain` | Prints `<commentID>`; `comment get <commentID> --field=comment_content` matches | `INSERT wp_comments`, `UPDATE wp_posts.comment_count` |
| WP-22 | W | `comment approve <commentID>` | `Success`; `comment get <commentID> --field=comment_approved` = `1` | `UPDATE wp_comments.comment_approved` |
| WP-23 | W | `comment meta add <commentID> gp_cm 'cm1'` | `Success`; `comment meta get <commentID> gp_cm` = `cm1` | `INSERT wp_commentmeta` |
| WP-24 | W | `comment delete <commentID> --force` | `Success`; `comment get <commentID>` errors; post `comment_count` decremented | `DELETE wp_comments`, `UPDATE wp_posts.comment_count` |

## F. Options, transients, site config (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-25 | W | `option add gp_opt 'gp_optval'` | `Success`; `option get gp_opt` = `gp_optval` | `INSERT wp_options` |
| WP-26 | W | `option update blogname 'goopg blog'` | `Success`; `option get blogname` = `goopg blog` | `UPDATE wp_options` |
| WP-27 | W | `option delete gp_opt` | `Success`; `option get gp_opt` errors/empty | `DELETE wp_options` |
| WP-28 | W | `option update gp_big "$(head -c 20000 /dev/zero \| tr '\0' x)"` | `Success`; `option get gp_big \| wc -c` ≈ 20000 — **exercises the TOAST path** (`root-0022`) | `UPDATE wp_options` (TOAST) |
| WP-29 | W | `transient set gp_tr 'trval' 3600` | `Success`; `transient get gp_tr` = `trval` | `INSERT`/`UPDATE wp_options (_transient_*)` |

## G. Plugins & themes (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-30 | W | `plugin activate hello` (Hello Dolly ships with core) | `Success`; `plugin list --status=active --field=name` includes `hello` | `UPDATE wp_options.active_plugins` |
| WP-31 | W | `plugin deactivate hello` | `Success`; `hello` no longer active | `UPDATE wp_options.active_plugins` |

## H. Raw SQL through PG4WP (write)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-32 | W | `db query "INSERT INTO wp_options (option_name,option_value,autoload) VALUES ('gp_raw','rawval','no')"`, then `db query "UPDATE wp_options SET option_value='rawval2' WHERE option_name='gp_raw'"`, then `db query "DELETE FROM wp_options WHERE option_name='gp_raw'"` | All exit 0; after each step `option get gp_raw` reflects it (rawval → rawval2 → empty) | `INSERT`/`UPDATE`/`DELETE wp_options` |

## I. Read-only verification (read)

| ID | W/R | Command (`wp …`) | Expected / PASS criterion | Tables / SQL touched |
|----|-----|------------------|---------------------------|----------------------|
| WP-R1 | R | `post list --format=count` | Prints integer ≥ seeded count | `SELECT wp_posts` |
| WP-R2 | R | `post get 1 --field=post_title` | Prints the welcome post title; no error | `SELECT wp_posts` |
| WP-R3 | R | `user list --format=table` | Lists at least `admin`; no SQL error | `SELECT wp_users`(+`wp_usermeta`) |
| WP-R4 | R | `term list category --format=count` | Prints integer; no error | `SELECT wp_terms ⋈ wp_term_taxonomy` |
| WP-R5 | R | `comment list --format=count` | Prints integer; no error | `SELECT wp_comments` |
| WP-R6 | R | `option get siteurl` | Prints `http://localhost:8080`; no error | `SELECT wp_options` |
| WP-R7 | R | `db query "SELECT COUNT(*) FROM wp_posts WHERE post_status='publish'"` | Prints a count; PG4WP passes SELECT through to goopg | `SELECT wp_posts` |
| WP-R8 | R | `core version` and `db size --tables` | Version prints; `db size` returns per-table sizes without error | catalog / size queries |

---

## Summary

| Group | IDs | Kind | Count |
|-------|-----|------|-------|
| A. Posts & pages | WP-01…06 | W | 6 |
| B. Post meta | WP-07…09 | W | 3 |
| C. Taxonomy | WP-10…15 | W | 6 |
| D. Users & roles | WP-16…20 | W | 5 |
| E. Comments | WP-21…24 | W | 4 |
| F. Options & transients | WP-25…29 | W | 5 |
| G. Plugins & themes | WP-30…31 | W | 2 |
| H. Raw SQL | WP-32 | W | 1 |
| I. Read-only | WP-R1…R8 | R | 8 |
| **Total** | | | **40** (32 W + 8 R) |

Each item's evidence — WP-CLI stdout/exit, the goopg statement-log slice, the
PG4WP SQL log slice, and the confirming read — is captured per
[`FLOW.md`](./FLOW.md).
