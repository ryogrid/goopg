#!/usr/bin/env python3
"""Value checksum for a psql result stream (M0124-0005).

Why this exists
---------------
The TPC-DS SF0.5 fast regression gate compares **row counts** against a
git-tracked PG 18.3 oracle, so it is structurally blind to "right row count,
wrong values".  Q75 is the worked example: before RC-1b goopg returned exactly
100 rows for Q75 -- matching PG -- while its `all_sales` CTE computed 1,057,469
against PG's 2,368,670.  `LIMIT 100` hid the corruption and the gate reported
PASS for weeks (design `docs/design/0124-0005-sf05-oracle-checksum-column.md`,
round-2 README S13.4 item 3).

This script turns one psql result stream into `rows` and `ck`, both derived
from the SAME parse of the SAME run -- a checksum captured in a different run
than its row count is not a fixture, it is two fixtures.

Normalisation (design D2), deliberately the same graded vocabulary that
`scripts/tpcds-value-diff.py` established for M0124-0006, because the two tools
must not disagree about what "the same value" means:

  * field-strip -- splits each row on '|' and strips each field.  Kills psql's
    column-width alignment AND goopg's un-blank-padded `char(n)` (ledger row
    2026-07-06, M0122-0005), neither of which is an answer difference.
  * numeric canon to **12 significant digits**.  Mandatory, and the reason is on
    record: ledger row `tpcds-round2 stddev-precision` documents goopg's
    `stddev_samp` (128-bit Newton-Raphson, 18 significant digits) diverging from
    PG's `sqrt_var` in the last 1-2 digits on 235 of 236 Q39 rows.  A naive byte
    checksum flags Q39 -- and every stddev/avg-bearing query -- on the first run.
    Canonicalising also collapses the numeric-scale gap (`0.00` vs
    `0.00000000000000000000`) that the same graded pass kills in value-diff.
    Twelve digits is far above the noise floor of the recorded divergence and
    far below the point where a real value defect could hide.

Column *names* are excluded from the hash: they come from the server's
RowDescription, and a label difference is a separate (already-tracked) defect
class, not a wrong answer.

`ck = n/a` (design D3)
----------------------
A query whose `ORDER BY` is not a total order under `LIMIT` has no stable row
SET: PG and goopg may legitimately return different members of the tie group
that straddles the window boundary.  Those queries stay row-count-only.

The rule implemented here is deliberately *conservative*, not exact: with
`--limits N[,N...]` (the LIMIT values appearing in the query file), any result
block whose row count equals one of them is treated as saturated -- rows were
discarded at the boundary -- and the whole query reports `ck=n/a`.  Saturation
is a necessary condition for boundary ambiguity, not a sufficient one, so this
over-approximates and can never manufacture a spurious CKMISMATCH.  A
non-saturated LIMIT returned everything, so its row set is complete and its
checksum is stable.

We do NOT sort before hashing.  Sorting would silently accept a wrong
*ordering*, which for a `LIMIT`-bearing TPC-DS query is itself a defect class.

Usage:
    scripts/tpcds-result-checksum.py <psql-output-file> [--limits 100,10]
prints one line:
    rows=<int> ck=<16-hex|n/a> blocks=<int>
"""

import argparse
import hashlib
import re
import sys
from decimal import Decimal, Context, InvalidOperation, ROUND_HALF_EVEN

SIG_DIGITS = 12
CTX = Context(prec=SIG_DIGITS, rounding=ROUND_HALF_EVEN)

# Only these render forms are canonicalised.  Decimal() also accepts 'NaN' and
# 'Infinity', which are legitimate *string* values in a result set, so the
# regex gate is what keeps them out of the numeric path.
NUMERIC_RE = re.compile(r"^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$")
TALLY_RE = re.compile(r"^\((\d+) rows?\)$")
RULE_RE = re.compile(r"^[-+]+$")


def canon(field):
    """Field-strip + numeric canon to SIG_DIGITS significant digits."""
    f = field.strip()
    if not NUMERIC_RE.match(f):
        return f
    try:
        d = Decimal(f)
    except (InvalidOperation, ValueError):
        return f
    if not d.is_finite():
        return f
    if d == 0:
        # '0', '0.00' and '-0' are the same answer; only the render differs.
        return "0"
    if "." not in f and "e" not in f and "E" not in f:
        # An integral render carries no float noise -- the recorded divergence is
        # in fractional digits -- so hash it EXACTLY.  Rounding it would blind
        # the gate to the low digits of large sums and ids for no benefit;
        # normalize() alone still equates '007' and '7'.
        return format(d, "f")
    # CTX.plus() rounds to SIG_DIGITS significant digits; normalize() then drops
    # trailing zeros so 1.50 and 1.5 hash alike, and format 'f' re-expands any
    # exponent form (1E+3 -> 1000) so notation never masks a value difference.
    return format(CTX.plus(d).normalize(), "f")


def parse_blocks(text):
    """Split psql aligned output into per-statement blocks of canonical rows.

    psql aligned form per result set is: a header line, a `---+---` rule, N data
    lines, a blank line, then `(N rows)`.  A file may hold several (TPC-DS Q14,
    Q23, Q24, Q39 are multi-statement).  A data value may in principle contain a
    newline, but no column in this corpus does, so a line-oriented parse is
    exact here.

    Returns [[row, ...], ...] where each row is a tuple of canonical fields.
    """
    blocks, rows, in_block = [], [], False
    for line in text.split("\n"):
        s = line.strip()
        if RULE_RE.match(s) and "-" in s:
            # The line before the rule was this block's header; drop it.
            in_block, rows = True, []
            continue
        if not in_block:
            continue
        m = TALLY_RE.match(s)
        if m:
            blocks.append(rows)
            in_block, rows = False, []
            continue
        # NO blank-line skip inside a block. psql emits the blank line AFTER the
        # `(N rows)` tally, not before it, so every line between the rule and the
        # tally is data — including a fully blank one, which is how a
        # single-column row holding NULL renders. Skipping blanks dropped
        # exactly that row: Q23 and Q92 (`sum` / `Excess Discount Amount` over an
        # empty match) both return one NULL row and were counted as 0.
        rows.append(tuple(canon(f) for f in line.split("|")))
    if in_block:
        # No tally line (psql --tuples-only, or a truncated capture).  Keeping
        # the block is right: dropping it would silently under-count rows.
        blocks.append(rows)
    return blocks


def checksum(blocks):
    """SHA-256 over the canonical row stream, truncated to 16 hex chars.

    Separators are ASCII unit/record/group separators so no field, row or block
    boundary can be forged by data containing the delimiter.
    """
    h = hashlib.sha256()
    for block in blocks:
        for row in block:
            h.update("\x1f".join(row).encode("utf-8"))
            h.update(b"\x1e")
        h.update(b"\x1d")
    return h.hexdigest()[:16]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("path")
    ap.add_argument(
        "--limits",
        default="",
        help="comma-separated LIMIT values in the query; a block whose row "
        "count equals one of them is saturated and forces ck=n/a (design D3)",
    )
    args = ap.parse_args()

    limits = {int(x) for x in args.limits.replace(",", " ").split() if x.isdigit()}
    with open(args.path, encoding="utf-8", errors="replace") as fh:
        blocks = parse_blocks(fh.read())

    rows = sum(len(b) for b in blocks)
    saturated = any(len(b) in limits for b in blocks)
    ck = "n/a" if saturated else checksum(blocks)
    print(f"rows={rows} ck={ck} blocks={len(blocks)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
