import re,sys
def rd(f): return [dict(re.findall(r'(\w+)=(\([^)]*\)|\S+)', l)) for l in open(f)]
P=rd(sys.argv[1]); G=rd(sys.argv[2])
rel=lambda a,b: abs(float(a)-float(b))/max(abs(float(a)),1)
for i,(p,g) in enumerate(zip(P,G)):
    flag = p['relids']!=g['relids'] or rel(p['rows'],g['rows'])>1e-6 or rel(p['startup'],g['startup'])>1e-6 or rel(p['total'],g['total'])>1e-6
    print('%2d %s %s rows %s/%s st %s/%s tot %s/%s %s/%s'%(i,'**' if flag else '  ',p['relids'],p['rows'],g['rows'],p['startup'],g['startup'],p['total'],g['total'],p.get('outer'),g.get('outer')))
