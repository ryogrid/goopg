# Milestones

This directory tracks scoped, sequential milestones for goopg. Each milestone has:

- A clearly bounded set of features to deliver.
- A set of design docs (under `../design/`) that must exist when the milestone is complete.
- A concrete Definition of Done with verifiable criteria.

Milestones are numbered with a 4-digit zero-padded prefix. Design-doc
filenames use `<milestone-or-spec-id>-NNNN-short-slug.md`: `root` for the
foundational requirements spec and the milestone ID (for example `0002`) for
milestone-scoped docs. `NNNN` is the per-identifier sequence.

The foundational requirements live in the top-level `REQUIREMENTS.md` at the repository root. That document plays the role of Milestone 0001 implicitly. New scope is captured in additional milestone documents in this directory.

## Status Values

- `planned` — Defined but not started.
- `in-progress` — Actively being implemented.
- `accepted` — Complete and merged. Definition of Done satisfied.
- `superseded` — Replaced by a later milestone. Document remains in place for history.
- `cancelled` — Abandoned, reason recorded inline.

When the agent begins work on a milestone, it must update the status field at the top of that milestone's file and update the table below in the same commit.

## Workflow Per Milestone

1. Read the milestone document and the relevant upstream sources under `./postgres/`.
2. Write the design docs listed under "Required Design Docs" first, with status `draft`.
3. Implement against those design docs. Update them to `accepted` when stable.
4. Verify every item in "Definition of Done".
5. Update milestone status to `accepted`.

## Index

| ID   | Title                                                  | Status   | Document                                          |
|------|--------------------------------------------------------|----------|---------------------------------------------------|
| 0001 | Foundational server (pgbench-able)                     | see root | `../../.ralph/specs/GOAL_AND_REQUIREMENTS.md`                           |
| 0002 | Production-grade checkpointing & concurrent B-tree     | planned  | `0002-durability-and-concurrent-storage.md`       |
| 0003 | HammerDB TPC-H workload coverage                       | planned  | `0003-tpch-workload.md`                           |
| 0004 | TAP test port & Go utility library                     | planned  | `0004-tap-test-port.md`                           |
| 0005 | Streaming replication support                          | planned  | `0005-streaming-replication-support.md`           |
