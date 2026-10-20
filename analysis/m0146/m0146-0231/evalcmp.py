import re,sys
def rd(f):
    out=[]
    for l in open(f):
        m=re.search(r'tour=([\d ]+?)\s+worth=([\d.e+]+)',l)
        if m: out.append((m.group(1).split(),float(m.group(2))))
    return out
P=rd(sys.argv[1]); G=rd(sys.argv[2])
same=sum(1 for p,g in zip(P,G) if p[0]==g[0])
first=next((i for i,(p,g) in enumerate(zip(P,G)) if p[0]!=g[0]),None)
print('sametour',same,'firsttourdiff',first)
n=0
for i,(p,g) in enumerate(zip(P,G)):
    if first is not None and i>=first: break
    r=abs(p[1]-g[1])/max(p[1],1)
    if r>1e-4:
        print(i,'pg %.10g go %.10g rel %.3g'%(p[1],g[1],r)); n+=1
        if n>12: break
