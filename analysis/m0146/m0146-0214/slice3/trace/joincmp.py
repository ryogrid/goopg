import re,sys
E=sys.argv[1]
def rd(f):
    out=[]
    for l in open(f):
        d=dict(re.findall(r'(\w+)=(\([^)]*\)|\S+)', l))
        out.append(d)
    return out
P=rd(f'tmp/m0214s3/pg-join{E}.txt'); G=rd(f'tmp/m0214s3/go-join{E}.txt')
for i,(p,g) in enumerate(zip(P,G)):
    if p['relids']!=g['relids'] or p['rows']!=g['rows'] or abs(float(p['startup'])-float(g['startup']))>5e-7 or abs(float(p['total'])-float(g['total']))>5e-7:
        print('first differing join', i)
        print(' PG', p); print(' GO', g)
        if i>0: print(' prev PG', P[i-1]['relids'], P[i-1]['total'], 'GO', G[i-1]['total'])
        break
else: print('all', len(P), 'joins equal')
