/*
 * Byte oracle for internal/utils/pgprng (M0146-0214): PG's own pg_prng.c,
 * linked from libpgcommon.a, prints one draw stream per seed. Regenerate the
 * goldens in pgprng_test.go with:
 *
 *   gcc -I postgres/src/include -o /tmp/prng_harness \
 *       internal/utils/pgprng/testdata/prng_harness.c \
 *       postgres/local_install/lib/libpgcommon.a \
 *       postgres/local_install/lib/libpgport.a -lm
 *   /tmp/prng_harness
 */
#include "postgres_fe.h"
#include "common/pg_prng.h"

static void
stream(const char *label, pg_prng_state *st)
{
	static const uint64 ranges[][2] = {
		{0, 0}, {0, 1}, {0, 4}, {1, 12}, {0, 999}, {5, 3}, {0, 2}, {1, 7}
	};

	printf("%s", label);
	for (int i = 0; i < 4; i++)
		printf(" u%llu", (unsigned long long) pg_prng_uint64(st));
	for (int i = 0; i < 4; i++)
		printf(" d%.17g", pg_prng_double(st));
	for (int i = 0; i < (int) lengthof(ranges); i++)
		printf(" r%llu", (unsigned long long)
			   pg_prng_uint64_range(st, ranges[i][0], ranges[i][1]));
	printf("\n");
}

int
main(int argc, char **argv)
{
	static const double fseeds[] = {0.0, 0.5, -0.25, 1.0, 0.123456789};
	static const uint64 seeds[] = {0, 12345};
	pg_prng_state st;
	char		label[64];

	for (int i = 0; i < (int) lengthof(fseeds); i++)
	{
		pg_prng_fseed(&st, fseeds[i]);
		snprintf(label, sizeof(label), "fseed=%.17g", fseeds[i]);
		stream(label, &st);
	}
	for (int i = 0; i < (int) lengthof(seeds); i++)
	{
		pg_prng_seed(&st, seeds[i]);
		snprintf(label, sizeof(label), "seed=%llu", (unsigned long long) seeds[i]);
		stream(label, &st);
	}
	return 0;
}
