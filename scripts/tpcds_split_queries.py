#!/usr/bin/env python3
"""Split monolithic TPC-DS query_0.sql into individual queryN.sql files.

Mimics the logic of split_sqls.py from tpcds-postgres.
Queries are separated by triple newlines in query_0.sql.
Output written to the current working directory.
"""
import sys, os, re

def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} query_0.sql", file=sys.stderr)
        sys.exit(1)

    blob_path = sys.argv[1]
    if not os.path.exists(blob_path):
        print(f"File not found: {blob_path}", file=sys.stderr)
        sys.exit(1)

    with open(blob_path, 'r', encoding='utf-8', errors='replace') as f:
        content = f.read()

    # Queries are separated by 3+ consecutive newlines
    chunks = re.split(r'\n\n\n+', content)
    n = 1
    for chunk in chunks:
        text = chunk.strip()
        if not text:
            continue
        out_path = f"query{n}.sql"
        with open(out_path, 'w', encoding='utf-8') as f:
            f.write(text)
            if not text.endswith('\n'):
                f.write('\n')
        n += 1

    # Remove query_0.sql since we've split it
    # os.unlink(blob_path)  # keep for reference
    print(f"Split {n-1} queries from {blob_path}", file=sys.stderr)

if __name__ == '__main__':
    main()
