import re,sys
node_re=re.compile(r'^\s*(?:->\s+)?([A-Z][A-Za-z ]+?(?: on \S+(?: \S+)?)?)(?:\s+\(|\s*$)')
act_re=re.compile(r'actual (?:time=\S+ )?rows=([\d.]+) loops=(\d+)')
def parse(path):
    nodes=[]
    for line in open(path):
        line=line.rstrip('\n')
        s=line.strip()
        if not s or s.startswith('QUERY PLAN') or s.startswith('---') or s.startswith('('): continue
        is_node = s.startswith('->') or (not nodes and not s.startswith('Planning') and not s.startswith('Execution'))
        m_act=act_re.search(s)
        if is_node:
            label=s[2:].strip() if s.startswith('->') else s
            label=label.split(' (')[0]
            tot=None
            if m_act and not s.startswith('Worker'): tot=float(m_act.group(1))*int(m_act.group(2))
            if 'never executed' in s: tot=0.0
            nodes.append([label,tot,0.0,False])
        elif nodes and s.startswith('Worker') and m_act:
            nodes[-1][2]+=float(m_act.group(1))*int(m_act.group(2)); nodes[-1][3]=True
    out=[]
    for label,tot,wsum,hasw in nodes:
        if tot is None: tot=wsum if hasw else None
        out.append((label,tot))
    return out
rows=[]
for q in range(1,100):
    try: g=parse(f'tmp/rowcensus/g{q}.txt'); p=parse(f'tmp/rowcensus/p{q}.txt')
    except FileNotFoundError: continue
    if len(g)!=len(p):
        print(f'Q{q}: node count differs g={len(g)} p={len(p)}'); continue
    for i,((gl,gt),(pl,pt)) in enumerate(zip(g,p)):
        if gt is None or pt is None: continue
        if gt>3*pt+1000 and gt-pt>5000:
            rows.append((gt-pt,q,i,gl,gt,pt,pl))
for d,q,i,gl,gt,pt,pl in sorted(rows,reverse=True)[:40]:
    print(f'Q{q} node{i} {gl[:55]:55} goopg={gt:.0f} pg={pt:.0f}' + ('' if gl.split()[0]==pl.split()[0] else f'  [pg: {pl[:40]}]'))
