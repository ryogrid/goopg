# goopg Design Documents

Design documents are **part of the deliverable** for this project, not scratch
notes. Every non-trivial subsystem must land alongside (or just before) its
design doc. See `.ralph/specs/GOAL_AND_REQUIREMENTS.md` §9 for the rules.

## Conventions

- Filenames use the form `NNNN-short-slug.md`, where `NNNN` is a zero-padded
  sequence number assigned at creation time and never reused.
- Each doc opens with a short metadata block: status, date, supersedes.
- Status values: `draft`, `accepted`, `superseded`, `historical`.
- When a new doc supersedes an older one, mark the older doc
  `superseded` and add a `superseded by:` link forward. Do not delete it.
- Cite upstream PostgreSQL with repository-relative paths
  (e.g. `postgres/src/backend/storage/buffer/bufmgr.c`).

## Index

| #    | Title                                         | Status   | Summary                                                                     |
| ---- | --------------------------------------------- | -------- | --------------------------------------------------------------------------- |
| 0001 | [Architecture Overview](0001-architecture-overview.md) | accepted | Single-process Go architecture, upstream-reference policy, reported `server_version`. |

Append new rows in numeric order. Do not reorder.
