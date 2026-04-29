# HammerDB TPC-H power test driver.
#
# Runs the standard TPC-H query suite (Q1..Q22) against the database
# populated by build_schema.tcl. With one virtual user this is the
# Power Test (single-stream) variant; HammerDB's queries are issued
# directly as SQL via the client connection — no server-side stored
# procedures are involved.

set tmpdir $::env(TMP)

puts "SETTING CONFIGURATION"
dbset db pg
dbset bm TPC-H

# Connection ---------------------------------------------------------------
diset connection pg_host       $::env(PG_HOST)
diset connection pg_port       $::env(PG_PORT)
diset connection pg_sslmode    disable

# Workload parameters ------------------------------------------------------
diset tpch pg_scale_fact            $::env(TPCH_SCALE_FACT)
diset tpch pg_tpch_user             $::env(TPCH_USER)
diset tpch pg_tpch_pass             $::env(TPCH_PASS)
diset tpch pg_tpch_dbase            $::env(TPCH_DB)
diset tpch pg_total_querysets       $::env(TPCH_TOTAL_QUERYSETS)
diset tpch pg_degree_of_parallel    $::env(TPCH_DEGREE_OF_PARALLEL)
diset tpch pg_raise_query_error     true
diset tpch pg_verbose               false
diset tpch pg_refresh_on            false

loadscript
puts "POWER TEST STARTED"
vuset vu 1
vucreate
set jobid [vurun]

# Wait for the (single) virtual user to finish before destroying it.
set finished 0
while {!$finished} {
    after 2000
    set vus [vustatus]
    set finished 1
    foreach status $vus {
        if {$status ne "FINISH SUCCESS" && $status ne "FINISHED SUCCESS" && $status ne ""} {
            set finished 0
            break
        }
    }
}
vudestroy

puts "POWER TEST COMPLETE (jobid=$jobid)"
set of [open "$tmpdir/pg_tproch_jobid" w]
puts $of $jobid
close $of
