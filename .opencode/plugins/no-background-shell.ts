import type { Plugin } from "@opencode-ai/plugin"

/**
 * Reject shell commands that would run in the background.
 *
 * Backgrounded shell work is killed when the session/turn ends, so a
 * backgrounded command silently loses its result. This hook fails any such
 * command before it runs and tells the agent to run it in the foreground.
 */

const BLOCK_MESSAGE =
  "Background shell execution is not allowed here. Run this command in the " +
  "FOREGROUND instead: remove any trailing '&' and do not use nohup, setsid, " +
  "or disown."

/** Strip single- and double-quoted regions so a literal '&' inside quotes is not misread. */
function stripQuoted(input: string): string {
  return input
    .replace(/'(?:[^'\\]|\\.)*'/g, "")
    .replace(/"(?:[^"\\]|\\.)*"/g, "")
}

function isBackgroundCommand(command: string): boolean {
  const bare = stripQuoted(command).trim()
  if (!bare) return false

  // Trailing '&' backgrounds the whole command line ('&&' at end is a syntax
  // error, not backgrounding).
  if (/&$/.test(bare) && !/&&$/.test(bare)) return true

  // Standalone '&' list separator (`cmd1 & cmd2`). Excluded: '&&', '&>', '>&',
  // '<&', '|&' — each has a second metacharacter touching the '&'.
  if (/(^|\s)&(\s|$)/.test(bare)) return true

  // Explicit detached / background launchers.
  if (/(^|[\s;&|])(nohup|setsid|disown)\b/.test(bare)) return true

  return false
}

const plugin: Plugin = async () => {
  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "bash") return
      const command = output.args?.command
      if (typeof command !== "string" || !isBackgroundCommand(command)) return
      throw new Error(BLOCK_MESSAGE)
    },
  }
}

export default plugin
