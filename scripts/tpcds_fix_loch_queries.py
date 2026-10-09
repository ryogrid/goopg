#!/usr/bin/env python3
"""Rewrite TPC-DS Q36/Q70/Q86 into the PostgreSQL-valid subquery form.

dsqgen emits these three queries with an ORDER BY that uses the output
alias `lochierarchy` inside an expression
(`case when lochierarchy = 0 then ... end`). PostgreSQL resolves a bare
output alias in ORDER BY but not one nested in an expression, so the raw
query fails with "column lochierarchy does not exist" on both engines.

The fix is the one third-party/tpcds-postgres/split_sqls.py applies: wrap
the SELECT ... GROUP BY ROLLUP block in `select * from (...) as sub` and
move the final ORDER BY (and its LIMIT) OUTSIDE the wrapper, where the
alias is an ordinary column of `sub`.

scripts/tpcds-setup.sh used to wrap the WHOLE file instead, leaving
`order by ... limit 100;` inside the parentheses. Every engine reports
`syntax error at or near ";"` on that text, so the three queries were
skipped by the regression oracle and showed up as capture errors in the
plan-parity fire set (M0146-0140). This script also repairs a file
already written in that broken form, and leaves a file already in the
fixed form untouched, so it is safe to run more than once.

usage: tpcds_fix_loch_queries.py <query file>...
"""

import sys

BROKEN_HEAD = "select * from (\n"
BROKEN_TAIL = ") as sub"
FIXED_MARK = ") as sub\n order by"


def fix(text):
    if FIXED_MARK in text:
        return text
    body = text
    if body.startswith(BROKEN_HEAD) and body.rstrip().endswith(BROKEN_TAIL):
        body = body[len(BROKEN_HEAD):].rstrip()[: -len(BROKEN_TAIL)]
    if "order by" not in body:
        raise ValueError("no final ORDER BY to move outside the wrapper")
    body = body.replace("select", "select * from (select ", 1)
    head, tail = body.rsplit("order by", 1)
    return (head + FIXED_MARK + tail).strip() + "\n"


def main(paths):
    for path in paths:
        with open(path) as f:
            text = f.read()
        fixed = fix(text)
        if fixed != text:
            with open(path, "w") as f:
                f.write(fixed)
            print("fixed %s" % path)
        else:
            print("ok    %s" % path)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    main(sys.argv[1:])
