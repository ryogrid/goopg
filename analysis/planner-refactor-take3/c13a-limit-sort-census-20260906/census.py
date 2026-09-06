#!/usr/bin/env python3
"""C-13a Probe P2 census: every Sort node in a goopg EXPLAIN ANALYZE capture,
with its ACTUAL input row count and its measured sort time, classified by
whether a Limit above it could supply a bound.

  usage: census.py <capture.txt> <tpcds query dir>

The capture is the file produced by the `capture.sh` recipe in README.md:
`===== Q<n> =====` separated blocks of `EXPLAIN (ANALYZE, VERBOSE OFF)` text
output, one block per TPC-DS query.

Three things this tool does that a regex over the plan text does not:

  * it rebuilds the real parent/child tree from the raw indent of each `->`,
    pushing a pseudo-node for the `CTE x` / `InitPlan n` / `SubPlan n` marker
    lines. Without that a CTE body reads as a direct child of the node above
    it and `Limit -> Sort` is scored wrong in both directions;
  * it reads the Sort's INPUT row count off the Sort's CHILD, not off the
    Sort. A Sort under a Limit stops early, so the Sort's own `actual rows`
    is the LIMIT, never the work it did;
  * it prices the sort as `Sort.actual_start - child.actual_end` — the child
    is drained before the first sorted row can be emitted, so that difference
    is the sort/heapify time itself. (Unreliable when loops > 1, where the
    two numbers are on different scales; every such row here has a tiny
    input and the noise is sub-10 ms.)

`bindable` classifies each Sort:
  direct  — it is the DIRECT child of a Limit: the shape C-13a would stamp
  descent — a Limit reaches it only through PG's ExecSetTupleBound descent
            whitelist (execProcnode.c:848-978: Append / MergeAppend /
            Result / qual-less SubqueryScan / Gather / GatherMerge)
  none    — no Limit can bound it at all
"""
import re, sys, os, json

NODE = re.compile(
    r'^(?P<pre>\s*)(?P<arrow>->\s{2})?(?P<name>[A-Z].*?)\s+'
    r'\(cost=(?P<sc>[\d.]+)\.\.(?P<tc>[\d.]+) rows=(?P<erows>\d+) width=(?P<w>\d+)\)'
    r'(?:\s+\((?:actual time=(?P<st>[\d.]+)\.\.(?P<et>[\d.]+) rows=(?P<arows>[\d.]+) loops=(?P<loops>[\d.]+)|never executed)\))?\s*$')
MARK = re.compile(r'^(?P<pre>\s*)(CTE \S+|InitPlan \d+.*|SubPlan \d+.*)\s*$')
SORTM = re.compile(r'^\s*Sort Method:\s*(?P<m>\S+(?: \S+)*?)(?:\s\s+(?P<sp>\w+): (?P<kb>\d+)kB)?\s*$')

# PG's ExecSetTupleBound descent whitelist (execProcnode.c:848-978), by the
# names goopg's EXPLAIN prints for them.
DESCEND = ('Append', 'Merge Append', 'Result', 'Subquery Scan', 'Gather', 'Gather Merge')

class N:
    def __init__(s, name, erows, w, arows, loops, indent, st, et):
        s.name=name; s.erows=erows; s.width=w; s.arows=arows; s.loops=loops
        s.indent=indent; s.st=st; s.et=et
        s.parent=None; s.children=[]; s.method=None; s.space=None; s.kb=None

def parse(body):
    roots=[]; stack=[]; last=None
    for ln in body.splitlines():
        m=NODE.match(ln)
        if m:
            indent=len(m.group('pre'))
            if not m.group('arrow') and indent>2: continue
            n=N(m.group('name').strip(), int(m.group('erows')), int(m.group('w')),
                float(m.group('arows')) if m.group('arows') else None,
                float(m.group('loops')) if m.group('loops') else None,
                indent,
                float(m.group('st')) if m.group('st') else None,
                float(m.group('et')) if m.group('et') else None)
            while stack and stack[-1][0]>=indent: stack.pop()
            if stack and isinstance(stack[-1][1],N):
                n.parent=stack[-1][1]; stack[-1][1].children.append(n)
            elif not stack:
                roots.append(n)
            stack.append((indent,n)); last=n; continue
        mk=MARK.match(ln)
        if mk:
            indent=len(mk.group('pre'))
            while stack and stack[-1][0]>=indent: stack.pop()
            stack.append((indent,'MARK')); last=None; continue
        sm=SORTM.match(ln)
        if sm and last is not None and 'Sort' in last.name:
            last.method=sm.group('m').strip(); last.space=sm.group('sp')
            last.kb=int(sm.group('kb')) if sm.group('kb') else None
    return roots

def walk(n):
    yield n
    for c in n.children: yield from walk(c)

def issort(n): return n.name.startswith('Sort') or n.name.startswith('Incremental Sort')

def sortfacts(c):
    inp=None; ms=None
    if c.children:
        ch=c.children[0]
        if ch.arows is not None: inp=ch.arows*(ch.loops or 1)
        if c.st is not None and ch.et is not None: ms=round((c.st-ch.et)*(c.loops or 1),1)
    return inp, ms

def limit_literal(qfile):
    try: txt=open(qfile,errors='replace').read()
    except OSError: return None
    m=re.findall(r'limit\s+(\d+)', txt, re.I)
    return int(m[-1]) if m else None

def classify(root):
    """Return {sortnode: 'direct'|'descent'|'none'}."""
    tag={}
    for n in walk(root):
        if issort(n): tag[id(n)]='none'
    for n in walk(root):
        if not n.name.startswith('Limit'): continue
        for c in n.children:
            if issort(c): tag[id(c)]='direct'
        # PG-style descent through nodes that neither discard nor combine rows
        stack=list(n.children)
        while stack:
            x=stack.pop()
            if issort(x):
                if tag.get(id(x))!='direct': tag[id(x)]='descent'
                continue
            if x.name.split('  ')[0].strip() in DESCEND:
                stack.extend(x.children)
    return tag

def main(path,qdir):
    text=open(path,errors='replace').read()
    blocks=re.split(r'^===== Q(\d+) =====$', text, flags=re.M)
    out=[]
    qs=set()
    for i in range(1,len(blocks),2):
        q=int(blocks[i]); qs.add(q); body=blocks[i+1]
        lit=limit_literal(os.path.join(qdir,f'query{q}.sql'))
        for root in parse(body):
            tag=classify(root)
            qms=root.et or 0
            for n in walk(root):
                if not issort(n): continue
                inp,ms=sortfacts(n)
                out.append(dict(q=q,limit=lit,kind=tag.get(id(n),'none'),
                                est=n.erows,inp=inp,out=n.arows,loops=n.loops,w=n.width,
                                ms=ms,qms=round(qms,0),method=n.method,space=n.space,kb=n.kb,
                                parent=n.parent.name if n.parent else '(root)',
                                child=n.children[0].name if n.children else None))
    return sorted(qs), out

if __name__=='__main__':
    qs,rows=main(sys.argv[1], sys.argv[2])
    print(f"queries in capture: {len(qs)}")
    for k in ('direct','descent','none'):
        sub=[r for r in rows if r['kind']==k]
        print(f"  Sorts, {k:>7}: {len(sub):>3}  (queries: {len(set(r['q'] for r in sub))})")
    print()
    hdr=f"{'Q':>4} {'kind':<8} {'lim':>4} {'est':>9} {'ACTUAL-IN':>10} {'out':>7} {'w':>5} {'sort-ms':>8} {'query-ms':>9} {'method':<15} {'kB':>8} child"
    print(hdr); print('-'*len(hdr))
    for r in sorted(rows,key=lambda r:-(r['inp'] or 0)):
        print(f"{('Q%d'%r['q']):>4} {r['kind']:<8} {str(r['limit']):>4} {r['est']:>9} "
              f"{('%d'%r['inp']) if r['inp'] is not None else '?':>10} "
              f"{('%d'%r['out']) if r['out'] is not None else '?':>7} {r['w']:>5} "
              f"{(str(r['ms']) if r['ms'] is not None else '?'):>8} {r['qms']:>9.0f} "
              f"{(r['method'] or '-'):<15} {str(r['kb'] or '-'):>8} {r['child']}")
    print()
    for k in ('direct','descent','none'):
        sub=[r for r in rows if r['kind']==k]
        tot=sum(r['ms'] or 0 for r in sub)
        print(f"  {k:>7}: n={len(sub):>3}  total sort ms={tot:>9.1f}  "
              f"max input={max([r['inp'] or 0 for r in sub] or [0]):>9.0f}  "
              f"spilled={sum(1 for r in sub if (r['method'] or '').startswith('external'))}")
    allq=sum(r['ms'] or 0 for r in rows)
    print(f"  ALL sorts: total sort ms={allq:.1f}")
    # corpus wall time
    qms={}
    for r in rows: qms[r['q']]=max(qms.get(r['q'],0), r['qms'])
    print(f"  sum of per-query root actual-time over queries WITH a sort: {sum(qms.values()):.0f} ms")
    for thr in (1000,10000,100000,1000000):
        print(f"  direct-child sorts with input >= {thr:>8}: "
              f"{sum(1 for r in rows if r['kind']=='direct' and (r['inp'] or 0)>=thr)}")
    json.dump(rows, open(sys.argv[1]+'.census.json','w'), indent=1)
