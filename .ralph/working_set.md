Task: M0120-0002 — Execute + capture write items WP-01…WP-16 (see .ralph/fix_plan.md).

Files:
- wp/verification/driver_wp01_16.sh (new) — full WP-01..WP-16 driver using
  wp/verification/run_item.sh's `run_item`/`baseline_snapshot`; ready to re-run
  as-is once unblocked.
- wp/verification/results/20260703-180343/ — partial run, only WP-01 has real
  evidence (a FAILURE); everything after is noise (empty postID cascades).
- .ralph/deferral_ledger.md — new row (M0120-0002) with full root-cause + fix.
- .ralph/fix_plan.md M0120-0002 — annotated BLOCKED, not checked.

Key symbols: wp/verification/run_item.sh `run_item`/`baseline_snapshot`;
wp-content/pg4wp/rewriters/CreateTableSQLRewriter.php (confirms PG4WP already
emits `bigserial` for `AUTO_INCREMENT` columns — not a PG4WP-side bug).

Hypothesis/Findings: CONFIRMED (not just hypothesis). Every WP core table's PK
column has zero indexes and zero column default (`SELECT indexname FROM
pg_indexes WHERE tablename='wp_posts'` → 0 rows; `pg_attrdef` empty for
`wp_posts.ID`/`wp_users.ID`/etc.). A fresh `CREATE TABLE ... bigserial ...
PRIMARY KEY(...)` against the SAME running :5544 instance works correctly
(sequence + default + PK all present) — ruled out a live goopg regression.
Conclusion: wp/goopg-data's WP tables are stale (created before current
DDL/serial fixes, or hit a since-fixed inline-PRIMARY-KEY-in-CREATE-TABLE bug)
and were never recreated because the data dir persists across restarts
(M0120-0001 explicitly preserved it).

Next step: need a human (or a session with the DROP pre-authorized) to run,
against :5544 (`LD_LIBRARY_PATH=postgres/local_install/lib
postgres/local_install/bin/psql -h 127.0.0.1 -p 5544 -U postgres -d postgres`):
  1. DROP TABLE wp_commentmeta, wp_comments, wp_links, wp_options, wp_postmeta,
     wp_posts, wp_term_relationships, wp_term_taxonomy, wp_termmeta, wp_terms,
     wp_usermeta, wp_users CASCADE;
  2. docker compose -f wp/docker-compose.yml run --rm -e WP_URL=http://localhost:8080 \
     -e WP_TITLE="goopg WordPress Blog" -e WP_ADMIN_PASSWORD="$(cat wp/.wp-admin-password)" \
     --entrypoint sh wpcli /seed/seed.sh   # re-installs + re-seeds in one step
  3. Re-run `wp/verification/driver_wp01_16.sh` (source run_item.sh, set
     RUN=wp/verification/results/$(date ...), mkdir -p "$RUN", then `bash
     wp/verification/driver_wp01_16.sh`) to (re)produce WP-01..16 evidence.
  4. Continue to M0120-0003 (WP-17..32) after 0002 is clean.
Once unblocked, this is NOT a new investigation — just re-run the existing driver.

Gates run: none applicable this loop (no engine code changed; pure harness
execution + diagnosis). `make ralph-state-guard` run before status block.
