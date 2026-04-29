# HammerDB TPC-H schema build script.
#
# This script is sourced by hammerdbcli with `auto`, which means the
# CLI will execute it and then exit. Connection settings, scale factor
# and the build-thread count are read from environment variables that
# the wrapper shell script populates so we never have to hard-code
# credentials in source control.
#
# TPC-H in HammerDB does not use stored procedures: schema build runs
# CREATE TABLE / INSERT / COPY statements straight from the client and
# the power test below sends each of the 22 queries as a regular SQL
# statement. There is no `pg_storedprocs` toggle for TPC-H — that flag
# only exists for TPC-C — so "no stored procedures" is the default and
# only mode for this workload.

puts "SETTING CONFIGURATION"
dbset db pg
dbset bm TPC-H

# Connection ---------------------------------------------------------------
diset connection pg_host          $::env(PG_HOST)
diset connection pg_port          $::env(PG_PORT)
diset connection pg_sslmode       disable

# Schema parameters --------------------------------------------------------
# `pg_scale_fact = 1` is the smallest scale factor HammerDB accepts for
# TPC-H. It produces ~1 GB of source data which loads in a few minutes
# on commodity hardware.
diset tpch pg_scale_fact          $::env(TPCH_SCALE_FACT)
diset tpch pg_num_tpch_threads    $::env(TPCH_BUILD_THREADS)
diset tpch pg_tpch_superuser      $::env(PG_SUPERUSER)
diset tpch pg_tpch_superuserpass  $::env(PG_SUPERUSER_PASS)
diset tpch pg_tpch_defaultdbase   postgres
diset tpch pg_tpch_user           $::env(TPCH_USER)
diset tpch pg_tpch_pass           $::env(TPCH_PASS)
diset tpch pg_tpch_dbase          $::env(TPCH_DB)
diset tpch pg_tpch_tspace         pg_default

puts "SCHEMA BUILD STARTED (scale factor = $::env(TPCH_SCALE_FACT), threads = $::env(TPCH_BUILD_THREADS))"
buildschema

# `buildschema` returns immediately; we have to poll the virtual users
# until they all finish loading data, otherwise the CLI exits before
# the build completes and the data ends up partial.
set finished 0
while {!$finished} {
    after 5000
    set vus [vustatus]
    set finished 1
    foreach status $vus {
        if {$status ne "FINISH SUCCESS" && $status ne "FINISHED SUCCESS" && $status ne ""} {
            set finished 0
            break
        }
    }
    puts "  vustatus: $vus"
}

vudestroy
puts "SCHEMA BUILD COMPLETED"
