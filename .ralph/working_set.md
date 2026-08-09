Task: M0130-S1 — Per-relation FSM/VM fork files (core implementation)

Files:
- internal/storage/fsm_fork.go: NEW — PG-compatible FSM fork writer/reader (buildFSMPage, buildFSMTree, WriteFSMFork, ReadFSMFork, FSMSaveForks, FSMLoadForks, RelForkPath)
- internal/storage/vm_fork.go: NEW — PG-compatible VM fork writer/reader (buildVMPage, parseVMPage, WriteVMFork, ReadVMFork, VMSaveForks, VMLoadForks)
- internal/storage/fsm_persistence_test.go: updated to fork-based API
- internal/storage/vm_persistence_test.go: updated to fork-based API
- internal/initdb/open.go: SaveVM/SaveFSM → VMSaveForks/FSMSaveForks; Load path → VMLoadForks/FSMLoadForks

Key symbols:
- RelForkPath(dataDir, rfn) string — public fork path helper (mirrors Manager.relPath)
- WriteFSMFork / ReadFSMFork — per-rel _fsm I/O (PG 3-level byte B-tree format)
- WriteVMFork / ReadVMFork — per-rel _vm I/O (PG visibility-map page format)
- buildFSMPage / buildFSMTree — PG-compatible FSM page construction
- FSMSaveForks / FSMLoadForks — bulk save/load on *FSM
- VMSaveForks / VMLoadForks — bulk save/load on *VisibilityMap

Hypothesis/Findings:
- FSM fork uses PG's fp_nodes binary tree + multi-level tree structure (level 0=leaf, higher=summaries)
- FSM categories: 256 levels, FSM_CAT_STEP=32 bytes, category 255 reserved for >=MaxFSMRequestSize(8164)
- Internal nodes can have children beyond fp_nodes array bounds → treat as 0 (fixed panic)
- VM fork uses 2 bits per heap page (ALL_VISIBLE + ALL_FROZEN) packed into page-aligned blocks
- Old Save/Load methods and FSMStatePath/VMStatePath still defined but dead code (no production callers)
- All gates PASS: storage tests, initdb tests, pgbench smoke (0 failures), tpch-spotcheck (Q12=2, Q13=35)

Next step: M0130-S1 cleanup — remove old aggregate Save/Load/FSMStatePath/VMStatePath, remove magic constants, handle BASE_BACKUP _fsm/_vm inclusion, update design doc status to accepted. Then M0130-S2 (pg_class heap persistence).

Gates run:
- go build ./...: PASS
- go test ./internal/storage/...: PASS
- go test ./internal/initdb/... ./internal/executor/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: PASS
- RALPH_PRECOMMIT_SCOPE=smoke: PASS (0 failed, all 3 workloads)
- scripts/tpch-spotcheck.sh: PASS (Q12=2, Q13=35)
- make ralph-state-guard: PASS

In-flight: none
