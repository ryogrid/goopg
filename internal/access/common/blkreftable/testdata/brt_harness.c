/*
 * brt_harness — drive PostgreSQL 18.3's src/common/blkreftable.c from a
 * script on stdin and write the serialized table to stdout.
 *
 * Script lines:
 *   M spc db rel fork blk        BlockRefTableMarkBlockModified
 *   L spc db rel fork limit      BlockRefTableSetLimitBlock
 *   R spc db rel fork from to    mark every block in [from, to)
 * End of input writes the table with WriteBlockRefTable.
 */
#include "postgres_fe.h"
#include "common/blkreftable.h"
#include "common/logging.h"

static int
write_cb(void *arg, void *data, int length)
{
	return fwrite(data, 1, length, stdout);
}

int
main(int argc, char **argv)
{
	BlockRefTable *brtab;
	char		op;
	unsigned	spc, db, rel, a, b;
	int			fork;

	pg_logging_init(argv[0]);
	brtab = CreateEmptyBlockRefTable();
	while (scanf(" %c %u %u %u %d %u", &op, &spc, &db, &rel, &fork, &a) == 6)
	{
		RelFileLocator rl;

		rl.spcOid = spc;
		rl.dbOid = db;
		rl.relNumber = rel;
		if (op == 'M')
			BlockRefTableMarkBlockModified(brtab, &rl, fork, a);
		else if (op == 'L')
			BlockRefTableSetLimitBlock(brtab, &rl, fork, a);
		else if (op == 'R')
		{
			if (scanf(" %u", &b) != 1)
				return 2;
			for (unsigned x = a; x < b; x++)
				BlockRefTableMarkBlockModified(brtab, &rl, fork, x);
		}
		else
			return 2;
	}
	WriteBlockRefTable(brtab, write_cb, NULL);
	fflush(stdout);
	return 0;
}
