(idle — nothing in flight)

Last loop (ralph2 #4): M0145-0028 DONE (code 6de31332c, docs follow) —
knob-arm TPC-DS Q41 floor match restored. Pre-flip readiness for M0145-0008:
executor-capability set empty; timing gap 1.01x (Q17, Q20 fixed); knob SF0.25
match=2 (= floor), TPC-H 2/22 (= default). Next per banner: M0145-0008 slice
"flip" — change the GOOPG_JOINTREE_PIPELINE default to on, then run the full
value gates + floor captures on the new default (legacy deletion is a later
slice). Check first how the knob default is read (grep jointreePipeline /
GOOPG_JOINTREE_PIPELINE, flaglabels.go) and which tests pin the default.
