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
 * Step 3df extension: in addition to the symbolic backtrace, emit
 * siginfo->si_addr (the faulting address — i.e. which pointer was
 * dereferenced) and the SysV-x86_64 argument / instruction / stack
 * registers (RDI, RSI, RDX, RAX, RIP, RSP) recovered from the
 * ucontext_t saved-register area. This lets a downstream investigator
 * attribute the crash to a specific function argument (typically
 * arg1=RDI, arg2=RSI) without rebuilding PG with debug symbols.
 *
 * This file MUST stay free of any state that could break PG normal
 * operation: a signal handler that only writes async-signal-safe
 * APIs (backtrace_symbols_fd, write, raise) and exits cleanly. The
 * hex-encoding helper below uses only stack-resident buffers and
 * `write(2)` — no printf, no malloc, no locale.
 */
#define _GNU_SOURCE
#include <execinfo.h>
#include <signal.h>
#include <stdint.h>
#include <string.h>
#include <unistd.h>
#if defined(__x86_64__)
#include <sys/ucontext.h>
#endif

#define MAX_FRAMES 128

/* Async-signal-safe: encode a 64-bit value as 16 hex chars (no NUL). */
static void hex16(uint64_t v, char out[16])
{
	static const char digits[] = "0123456789abcdef";
	for (int i = 15; i >= 0; --i) {
		out[i] = digits[v & 0xf];
		v >>= 4;
	}
}

/* Write "<label>0x<16 hex>" to stderr without allocating. `labellen` is
 * the exact byte count of `label` so the caller controls trailing/
 * leading spacing without strlen. */
static void write_reg(const char *label, size_t labellen, uint64_t v)
{
	char buf[18];
	buf[0] = '0';
	buf[1] = 'x';
	hex16(v, buf + 2);
	(void) !write(STDERR_FILENO, label, labellen);
	(void) !write(STDERR_FILENO, buf, 18);
}

static void segv_handler(int sig, siginfo_t *info, void *ucontext)
{
	static const char hdr[] = "\n[GOOPG_SEGV_BACKTRACE] SIGSEGV caught, backtrace follows:\n";
	(void) !write(STDERR_FILENO, hdr, sizeof(hdr) - 1);

	/* si_addr — the faulting address. For a NULL-deref this is 0;
	 * for an unmapped pointer it is the bad pointer itself. */
	{
		static const char prefix[] = "[GOOPG_SEGV_BACKTRACE] si_addr=";
		uint64_t addr = (uint64_t) (uintptr_t) (info ? info->si_addr : (void *) 0);
		(void) !write(STDERR_FILENO, prefix, sizeof(prefix) - 1);
		write_reg("", 0, addr);
		(void) !write(STDERR_FILENO, "\n", 1);
	}

#if defined(__x86_64__)
	/* Saved registers from the ucontext_t mcontext area. SysV-AMD64
	 * ABI: args 1..3 → RDI/RSI/RDX, return value → RAX, instruction
	 * pointer → RIP, stack pointer → RSP. Together these are usually
	 * enough to attribute a SIGSEGV to a specific call argument. */
	if (ucontext) {
		ucontext_t *uc = (ucontext_t *) ucontext;
		uint64_t rdi = (uint64_t) uc->uc_mcontext.gregs[REG_RDI];
		uint64_t rsi = (uint64_t) uc->uc_mcontext.gregs[REG_RSI];
		uint64_t rdx = (uint64_t) uc->uc_mcontext.gregs[REG_RDX];
		uint64_t rax = (uint64_t) uc->uc_mcontext.gregs[REG_RAX];
		uint64_t rip = (uint64_t) uc->uc_mcontext.gregs[REG_RIP];
		uint64_t rsp = (uint64_t) uc->uc_mcontext.gregs[REG_RSP];

		static const char regs_prefix[] = "[GOOPG_SEGV_BACKTRACE] regs:";
		(void) !write(STDERR_FILENO, regs_prefix, sizeof(regs_prefix) - 1);
		write_reg(" RDI=", 5, rdi);
		write_reg(" RSI=", 5, rsi);
		write_reg(" RDX=", 5, rdx);
		write_reg(" RAX=", 5, rax);
		write_reg(" RIP=", 5, rip);
		write_reg(" RSP=", 5, rsp);
		(void) !write(STDERR_FILENO, "\n", 1);
	}
#else
	(void) ucontext;
#endif

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
