#!/usr/bin/env python3
"""Aggregate E-11 depth-sweep arm files into a per-query table + summary."""
import re, os, statistics as st

SP = "/tmp/claude-1000/-home-ryo-work-goopg-goopg/b8e0d095-04da-41e7-b272-805106e9cf98/scratchpad/e11b"
DEPTHS = [0, 4, 16, 64, 128]
REPS = [1, 2, 3]

LINE = re.compile(r"^(Q[0-9A-Za-z-]+): OK elapsed=([\d.]+)s colsig=(\S+) ordered=(\S+) unordered=(\S+) rows=(\d+)")
BAD = re.compile(r"^(Q[0-9A-Za-z-]+): (\S+)")


def qkey(q):
    m = re.match(r"Q(\d+)(.*)", q)
    return (int(m.group(1)), m.group(2)) if m else (999, q)


def load(path):
    t, d = {}, {}
    for line in open(path):
        m = LINE.match(line)
        if m:
            t[m.group(1)] = float(m.group(2))
            d[m.group(1)] = (m.group(3), m.group(4), m.group(5), m.group(6))
            continue
        m2 = BAD.match(line)
        if m2 and m2.group(2) != "OK":
            t[m2.group(1)] = None
    return t, d


arms, digs = {}, {}
for dep in DEPTHS:
    for r in REPS:
        p = f"{SP}/d{dep}-r{r}.txt"
        if os.path.exists(p):
            t, d = load(p)
            if t:
                arms[(dep, r)] = t
                digs[(dep, r)] = d

qs = sorted({q for t in arms.values() for q in t}, key=qkey)
print("arms present:", sorted(arms.keys()))
print(f"queries per arm: {len(qs)}")
print()

ref = None
refk = None
mismatch = []
for k, d in sorted(digs.items()):
    if ref is None:
        ref, refk = d, k
        continue
    for q in sorted(set(ref) | set(d), key=qkey):
        if ref.get(q) != d.get(q):
            mismatch.append((k, q, ref.get(q), d.get(q)))
print(f"VALUES vs {refk}: {'ALL ARMS IDENTICAL' if not mismatch else 'MISMATCH'} "
      f"({len(digs)} arms x {len(ref)} queries)")
for m in mismatch[:10]:
    print("  ", m)
print()

print(f"{'Q':<14}" + "".join(f"  d{dep}r{r}" for dep in DEPTHS for r in REPS))
for q in qs:
    row = f"{q:<14}"
    for dep in DEPTHS:
        for r in REPS:
            v = arms.get((dep, r), {}).get(q)
            row += f"  {v:6.2f}" if v is not None else "     --"
    print(row)
print()

print("depth   median suite total    per-rep totals")
base_med = {}
for dep in DEPTHS:
    tots = []
    for r in REPS:
        t = arms.get((dep, r))
        if t and all(t.get(q) is not None for q in qs):
            tots.append(sum(t[q] for q in qs))
    if tots:
        base_med[dep] = st.median(tots)
        print(f"  d{dep:<5} {st.median(tots):9.2f}s          " + "  ".join(f"{x:.2f}" for x in tots))
print()

print("A/A noise band -- control (depth 4) rep-vs-rep spread per query:")
worst = []
for q in qs:
    vals = [arms[(4, r)][q] for r in REPS if (4, r) in arms and arms[(4, r)].get(q)]
    if len(vals) >= 2:
        lo, hi = min(vals), max(vals)
        worst.append((100.0 * (hi - lo) / lo, q, lo, hi))
worst.sort(reverse=True)
for pct, q, lo, hi in worst:
    print(f"  {q:<14} spread {pct:5.1f}%   ({lo:.2f} .. {hi:.2f})")
if worst:
    print(f"  => worst control-vs-control per-query spread: {worst[0][0]:.1f}% ({worst[0][1]})")
    print(f"  => median control-vs-control per-query spread: {st.median([w[0] for w in worst]):.1f}%")
print()

print("per-query median-of-reps by depth, and % vs depth 4:")
print(f"{'Q':<14}" + "".join(f"{'d'+str(d):>9}" for d in DEPTHS)
      + "   |" + "".join(f"{'d'+str(d):>9}" for d in DEPTHS if d != 4))
for q in qs:
    med = {}
    for dep in DEPTHS:
        vals = [arms[(dep, r)][q] for r in REPS if (dep, r) in arms and arms[(dep, r)].get(q)]
        if vals:
            med[dep] = st.median(vals)
    if 4 not in med:
        continue
    row = f"{q:<14}" + "".join(f"{med[d]:9.2f}" if d in med else "       --" for d in DEPTHS)
    row += "   |" + "".join(f"{100*(med[d]-med[4])/med[4]:+8.1f}%" for d in DEPTHS if d != 4 and d in med)
    print(row)
print()
if 4 in base_med:
    print("suite total vs depth 4 (median of reps):")
    for dep in DEPTHS:
        if dep in base_med:
            print(f"  d{dep:<4} {base_med[dep]:8.2f}s  {100*(base_med[dep]-base_med[4])/base_med[4]:+6.2f}%")
