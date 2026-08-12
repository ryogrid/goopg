# Blank-Split Table Sample

Reproduces the two structural defects found in `.ralph/deferral_ledger.md`:
blank lines splitting the table body, and a row that lost its trailing pipe.

| status | date | task-id | note |
|--------|------|---------|------|
| - | 2026-08-09 | M0129-S5.7 | first row, well formed |
| - | 2026-08-09 | M0129-S5.8 | last row before the split |

| - | 2026-08-09 | M0119-0011 | orphaned by the blank line above |

| - | 2026-08-09 | M0119-0004 | orphaned too |
| - | 2026-08-10 | M0130-S9 | this row lost its trailing pipe
| resolved | | 2026-08-11 | M0119-0006 | doubled separator shifts the columns |

Trailing paragraph — must stay outside the table.
