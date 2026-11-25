#!/bin/bash
# build-pg183-toolchain.sh — build the PG 18.3 test-module toolchain under
# tmp/pg183-inj: the same source as postgres/ (git archive of the submodule
# HEAD), configured as local_install is (--enable-cassert --enable-tap-tests)
# plus --enable-injection-points, with the contrib and test modules recovery
# TAP tests load (pg_prewarm, test_decoding, injection_points).
#
# postgres/ and postgres/local_install are the read-only oracle and are never
# touched: the source is extracted to tmp/pg183-src and installed to its own
# prefix. Mixed-pairing TAP ports that CREATE EXTENSION one of these modules
# run their PG 18.3 nodes from here (tapnode.ModulesBinDir). M0149-0029.
#
# Usage: scripts/build-pg183-toolchain.sh [--force]
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
SRC=$ROOT/tmp/pg183-src
PREFIX=$ROOT/tmp/pg183-inj
MODULES="contrib/pg_prewarm contrib/test_decoding src/test/modules/injection_points"
if [[ "${1:-}" != "--force" && -x $PREFIX/bin/postgres ]]; then
	missing=0
	for ext in pg_prewarm test_decoding injection_points; do
		[[ -e $PREFIX/share/postgresql/extension/$ext.control || $ext == test_decoding ]] || missing=1
	done
	[[ -e $PREFIX/lib/postgresql/test_decoding.so ]] || missing=1
	if [[ $missing == 0 ]]; then
		echo "pg183 toolchain present at $PREFIX"
		exit 0
	fi
fi
rm -rf "$SRC"
mkdir -p "$SRC"
git -C postgres archive HEAD | tar -x -C "$SRC"
cd "$SRC"
./configure --enable-cassert --enable-tap-tests --enable-injection-points --prefix="$PREFIX"
make -j"$(nproc)"
make install
for m in $MODULES; do
	make -C "$m" install
done
echo "pg183 toolchain built at $PREFIX"
