#!/bin/bash
# S3 — summarize: classification, summary.json/md, action-items.md, history,
# retention. Thin wrapper; the logic lives in lib/summarize.py (ci/design/04, 07).
# Exit code IS the batch verdict: 0 pass / 2 fail / 3 inconclusive.
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

progress "S3" "summarize start"
rc=0
python3 "${REPO_ROOT}/ci/batch/lib/summarize.py" \
    --run-dir "${RUN_DIR}" --repo-root "${REPO_ROOT}" || rc=$?
progress "S3" "summarize done (rc=${rc})"
exit ${rc}
