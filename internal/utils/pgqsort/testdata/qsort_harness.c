/*
 * Byte oracle for internal/utils/pgqsort (M0146-0214): PG's own pg_qsort,
 * linked from libpgport.a, sorts GEQO-shaped elements (a double worth plus an
 * id) with geqo_pool.c's compare(). Each line prints the input worths and the
 * resulting id order, so ties expose the exact permutation. Regenerate
 * testdata/qsort_golden.txt with:
 *
 *   gcc -I postgres/src/include -o /tmp/qsort_harness \
 *       internal/utils/pgqsort/testdata/qsort_harness.c \
 *       postgres/local_install/lib/libpgcommon.a \
 *       postgres/local_install/lib/libpgport.a -lm
 *   /tmp/qsort_harness > internal/utils/pgqsort/testdata/qsort_golden.txt
 */
#include "postgres_fe.h"
#include "common/pg_prng.h"

typedef struct
{
	double		worth;
	int			id;
} Elem;

/* geqo_pool.c compare() */
static int
compare(const void *arg1, const void *arg2)
{
	const Elem *c1 = (const Elem *) arg1;
	const Elem *c2 = (const Elem *) arg2;

	if (c1->worth == c2->worth)
		return 0;
	else if (c1->worth > c2->worth)
		return 1;
	else
		return -1;
}

static void
run(int n, int shape, uint64 k, pg_prng_state *st)
{
	Elem	   *e = malloc(sizeof(Elem) * (n > 0 ? n : 1));

	for (int i = 0; i < n; i++)
	{
		e[i].id = i;
		if (shape == 0)
			e[i].worth = (double) pg_prng_uint64_range(st, 0, k);
		else if (shape == 1)
			e[i].worth = (double) (i / 3);	/* presorted with ties */
		else
			e[i].worth = (double) ((n - i) / 2);	/* reversed with ties */
	}
	printf("n=%d w=", n);
	for (int i = 0; i < n; i++)
		printf("%s%g", i ? "," : "", e[i].worth);
	qsort(e, n, sizeof(Elem), compare);
	printf(" ids=");
	for (int i = 0; i < n; i++)
		printf("%s%d", i ? "," : "", e[i].id);
	printf("\n");
	free(e);
}

int
main(int argc, char **argv)
{
	static const int sizes[] = {0, 1, 2, 5, 6, 7, 8, 9, 20, 40, 41, 50, 100, 250, 500};
	static const uint64 ties[] = {1, 4, 1000000};
	pg_prng_state st;

	pg_prng_seed(&st, 214);
	for (int s = 0; s < (int) lengthof(sizes); s++)
	{
		for (int t = 0; t < (int) lengthof(ties); t++)
			run(sizes[s], 0, ties[t], &st);
		run(sizes[s], 1, 0, &st);
		run(sizes[s], 2, 0, &st);
	}
	return 0;
}
