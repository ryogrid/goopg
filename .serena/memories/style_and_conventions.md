# goopg Code Style and Conventions

## General
- Standard Go idioms; gofmt must pass
- No CGo unless unavoidable (must justify in a design doc)
- No comments explaining WHAT code does — only WHY (non-obvious constraints, workarounds)
- No docstrings / multi-line comment blocks

## Naming
- Packages: lowercase, single word where possible
- Functions/methods: CamelCase (Go standard)
- Constants: CamelCase for exported, camelCase for unexported
- Test files: `*_test.go` next to the code they test; oracle/TAP tests in `internal/testport/`

## Design Docs
- Required for every non-trivial subsystem change
- Path: `docs/design/<milestone-or-spec-id>-NNNN-short-slug.md`
  - Examples: `root-0001-architecture-overview.md`, `0094-0005c-standby-mvcc-visibility.md`
- Must be indexed in `docs/design/README.md` in the same commit
- Status field: draft → accepted

## WAL Record Kinds
Physical page records replay via `ApplyRecord`. Logical-only records (XactCommit, XactAbort, CreateIndex, etc.) are no-ops in `ApplyRecord` and handled by separate passes (recovery driver, StreamReplayer hook).

## MVCC Visibility
- Hot read path: `TupleVisible → snap.SeesCommittedXID` using Xmin/Xmax/InProgress/Aborted only
- Clog consulted only in cold-start `loadUserTablesFromHeap`
- Standby: `ReplayXactCommit/ReplayXactAbort` advance nextXID so replayed tuples are visible

## Test Discipline
- Oracle TAP tests live in `internal/testport/` — never run as part of default `go test ./...`
- Integration tests: build tag `//go:build integration`
- Pre-commit gates for planner/executor/wal changes: run key package tests + `make ralph-state-guard`
