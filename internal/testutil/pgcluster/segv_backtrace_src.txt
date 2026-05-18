/*
 * segv_backtrace.c — diagnostic-only LD_PRELOAD shim for goopg E2E tests.
 *
 * Why this exists: when the upstream PostgreSQL standby spawned by
 * goopg E2E tests is crashed by a SIGSEGV in a client backend, PG's
 * postmaster logs only "client backend (PID N) was terminated by
 * signal 11: Segmentation fault" with no backtrace. PG installs no
 * SIGSEGV handler of its own (libpq/pqsignal.c only sigdelsets SIGSEGV
 * from BlockSig), so an LD_PRELOAD'd handler will fire before the
 * kernel terminates the child. We write backtrace_symbols_fd output
 * to STDERR (which is the postmaster log file in pgcluster.Start),
 * then restore SIG_DFL and re-raise so the kernel still produces the
 * same exit status the postmaster expects to harvest.
 *
 * This file MUST stay free of any state that could break PG normal
 * operation: a signal handler that only writes async-signal-safe
 * APIs (backtrace_symbols_fd, write, raise) and exits cleanly.
 */
#define _GNU_SOURCE
#include <execinfo.h>
#include <signal.h>
#include <string.h>
#include <unistd.h>

#define MAX_FRAMES 128

static void segv_handler(int sig, siginfo_t *info, void *ucontext)
{
	(void) info;
	(void) ucontext;
	static const char hdr[] = "\n[GOOPG_SEGV_BACKTRACE] SIGSEGV caught, backtrace follows:\n";
	(void) !write(STDERR_FILENO, hdr, sizeof(hdr) - 1);
	void *frames[MAX_FRAMES];
	int n = backtrace(frames, MAX_FRAMES);
	backtrace_symbols_fd(frames, n, STDERR_FILENO);
	static const char ftr[] = "[GOOPG_SEGV_BACKTRACE] end of backtrace\n";
	(void) !write(STDERR_FILENO, ftr, sizeof(ftr) - 1);

	struct sigaction sa;
	memset(&sa, 0, sizeof(sa));
	sa.sa_handler = SIG_DFL;
	sigemptyset(&sa.sa_mask);
	sigaction(sig, &sa, NULL);
	raise(sig);
}

__attribute__((constructor))
static void install_segv_handler(void)
{
	struct sigaction sa;
	memset(&sa, 0, sizeof(sa));
	sa.sa_sigaction = segv_handler;
	sigemptyset(&sa.sa_mask);
	sa.sa_flags = SA_SIGINFO | SA_RESETHAND | SA_NODEFER;
	sigaction(SIGSEGV, &sa, NULL);
}
