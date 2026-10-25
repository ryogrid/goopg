#!/usr/bin/env python3
"""Render README §6 tables (release-build PG vs goopg) from bench/pg-release/results."""
import csv, math, re, statistics, sys

B = "/home/ryo/work/goopg/goopg/bench/pg-release/results"
A = "/home/ryo/work/goopg/goopg/analysis/timing-after-planprity-match/data"
HEAVY = {"Q1", "Q4", "Q6", "Q11", "Q54", "Q74"}


def stats(pairs):
    pg = sum(p for p, g in pairs); gp = sum(g for p, g in pairs)
    rs = [g / p for p, g in pairs]
    gm = math.exp(sum(map(math.log, rs)) / len(rs))
    return dict(n=len(rs), pg=pg, goopg=gp, sum=gp / pg, gm=gm, med=statistics.median(rs),
                le1=sum(r <= 1.0 for r in rs))


def fmt(v):
    return f"{int(round(v)):,}"


out = []
# ---------------- TPC-H ----------------
rows = list(csv.DictReader(open(f"{B}/tpch/tpch-pgrel.tsv"), delimiter="\t"))
by = {r["query"]: r for r in rows}
out.append("<!-- TPCH -->")
out.append("| クエリ | PG 18.3 本番ビルド (ms) | goopg (ms) | 対PG比 (goopg/PG) | 参考: 3 回の pass 2 中央値 / 最小〜最大 (ms) | 参考: §2.2 の cassert PG (ms) |")
out.append("|---|--:|--:|--:|--:|--:|")
pairs, cass = [], []
for i in range(1, 23):
    q = f"Q{i}"
    if q == "Q15":
        out.append("| Q15 | — | — | — | — | — |")
        continue
    r = by[q]
    p, g = float(r["pgrel_rep1_pass2_ms"]), float(r["goopg_ms"])
    pairs.append((p, g)); cass.append(float(r["pg_cassert_ms"]))
    out.append(f"| {q} | {fmt(p)} | {fmt(g)} | {g/p:.2f} | {fmt(float(r['pgrel_median3_pass2_ms']))} / "
               f"{fmt(float(r['pgrel_min_pass2_ms']))}〜{fmt(float(r['pgrel_max_pass2_ms']))} | {fmt(float(r['pg_cassert_ms']))} |")
s = stats(pairs)
out.append(f"| **合計 ({s['n']} クエリ, Q15 除く)** | **{fmt(s['pg'])}** | **{fmt(s['goopg'])}** | **{s['sum']:.2f}** | "
           f"{fmt(sum(float(by[q]['pgrel_median3_pass2_ms']) for q in by))} | {fmt(sum(cass))} |")
out.append("")
out.append(f"TPCH-STATS {s}")
out.append("")

# ---------------- TPC-DS SF0.25 ----------------
def sweep(path):
    d = {}
    for l in open(path):
        m = re.match(r"^(Q\d+)\s+(\S+)\s+(\d+)ms", l)
        if m:
            d[m.group(1)] = (m.group(2), int(m.group(3)))
    return d

sw = [sweep(f"{B}/tpcds025/sweep{i}.txt") for i in (1, 2, 3)]
old = {r["query"]: r for r in csv.DictReader(open(f"{A}/tpcds-sf025.tsv"), delimiter="\t")}
out.append("<!-- SF025 -->")
out.append("| クエリ | PG 18.3 本番ビルド (ms) | goopg (ms) | 対PG比 (goopg/PG) | 備考 |")
out.append("|---|--:|--:|--:|---|")
pairs, light, tsv = [], [], []
for i in range(1, 100):
    q = f"Q{i}"
    vals = [s_[q][1] for s_ in sw]
    verdicts = {s_[q][0] for s_ in sw}
    p = statistics.median(vals); g = float(old[q]["goopg_ms"])
    pairs.append((p, g))
    if q not in HEAVY:
        light.append((p, g))
    note = []
    if verdicts != {"PASS"}:
        note.append("判定: " + "/".join(sorted(verdicts)))
    if q in ("Q14", "Q23", "Q24", "Q39"):
        note.append("a/b 2 文を 1 ファイルとして合算計測")
    if q in HEAVY:
        note.append("PG が極端に遅い 6 本の 1 つ")
    out.append(f"| {q} | {fmt(p)} | {fmt(g)} | {g/p:.2f} | {'; '.join(note)} |")
    tsv.append(f"{q}\t{vals[0]}\t{vals[1]}\t{vals[2]}\t{p:.0f}\t{g:.0f}\t{old[q]['pg_ms']}\t{'/'.join(sorted(verdicts))}")
s = stats(pairs); sl = stats(light)
out.append(f"| **合計 (99 クエリ)** | **{fmt(s['pg'])}** | **{fmt(s['goopg'])}** | **{s['sum']:.2f}** | |")
out.append("")
out.append(f"SF025-STATS {s}")
out.append(f"SF025-LIGHT {sl}")
out.append("SWEEP-TOTALS " + str([sum(v[1] for v in s_.values()) for s_ in sw]))
old_pg = sum(float(old[q]["pg_ms"]) for q in old)
out.append(f"OLD-PG-TOTAL {old_pg}")
with open(f"{B}/tpcds025/tpcds025-pgrel.tsv", "w") as f:
    f.write("query\tpgrel_sweep1_ms\tpgrel_sweep2_ms\tpgrel_sweep3_ms\tpgrel_median_ms\tgoopg_ms\tpg_cassert_ms\tverdicts\n")
    f.write("\n".join(tsv) + "\n")

# buckets
def buckets(pairs):
    b = [0] * 6
    for p, g in pairs:
        r = g / p
        b[0 if r <= 1 else 1 if r <= 1.5 else 2 if r <= 2 else 3 if r <= 3 else 4 if r <= 5 else 5] += 1
    return b
out.append("BUCKETS-SF025 " + str(buckets(pairs)))
top = sorted(((g / p, q) for (p, g), q in zip(pairs, [f"Q{i}" for i in range(1, 100)])), reverse=True)[:10]
out.append("TOP-SF025 " + ", ".join(f"{q}={r:.2f}" for r, q in top))
low = [q for (p, g), q in zip(pairs, [f"Q{i}" for i in range(1, 100)]) if g / p <= 1]
out.append("LE1-SF025 " + ",".join(low))
print("\n".join(out))
