(idle — nothing in flight)

Last loop (#28): M0119-0004 **`NOT VALID` FOREIGN KEY round-trip in pg_dump**
(DU-002 slice 307) — LANDED. Design `0119-0004-fk-not-valid-roundtrip.md`.

PG's `pg_get_constraintdef_worker` (ruleutils.c:2604) appends a trailing
` NOT VALID` for `pg_constraint.convalidated='f'` in the SHARED tail common to
every constraint type, AFTER the DEFERRABLE/INITIALLY DEFERRED clauses. pg_dump's
getConstraints renders each FK via `pg_get_constraintdef`, so the suffix rides the
`ALTER TABLE ONLY … ADD CONSTRAINT …;`. goopg already tracked the state
end-to-end (parser `act.NotValid` → `catalog.ForeignKey.NotValid` →
`pg_constraint` virtual builder `convalidated='f'` at catalog.go:4938) but the
deparse `buildForeignKeyDefString` (expr.go) never emitted the tail → a NOT-VALID
FK dumped without it and silently re-validated on restore. Fix = append
` NOT VALID` after the DEFERRABLE block when `fk.NotValid` (1-line logic add).

Gates: new DU-002 slice 307 in `TestPort_PgDumpConnectionSetup` (`nv_child` FK →
`ADD CONSTRAINT nv_child_fk FOREIGN KEY (ref_id) REFERENCES public.nv_ref(id)
NOT VALID;` asserted in stdout) PASS; executor+parser+catalog suites PASS;
`go build ./...` clean; pgbench smoke = pre-commit hook.

NEXT loop — remaining open under M0119-0004 (probe TestPort_PgDumpConnectionSetup
for the next getter-battery gap):
- **CHECK … NOT VALID round-trip** — needs a new `NotValid` field on
  `catalog.NamedCheckConstraint` + `convalidated` projection for contype='c'
  (CHECK rows hardcode convalidated='t' at catalog.go ~4715/4756). The
  pg_get_constraintdef CHECK branch (expr.go ~7054 `CHECK ((expr))`) must append
  ` NOT VALID` like the FK path now does. Parser already accepts CHECK NOT VALID.
- **FK MATCH FULL round-trip** — parser/deparse do not surface the FK match type;
  `buildForeignKeyDefString` always omits MATCH (only MATCH SIMPLE default today).
- extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement; see memory).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).

NOTE: the ` NOT VALID` tail is the SHARED final clause of
pg_get_constraintdef_worker — applies to FK *and* CHECK (and others). This loop
wired only the FK path (FK already had the catalog flag); CHECK needs the new
field first. Order in def string: REFERENCES → ON UPDATE → ON DELETE →
DEFERRABLE → INITIALLY DEFERRED → NOT VALID (matches PG byte order).
