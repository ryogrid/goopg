import type { Plugin } from "@opencode-ai/plugin"
import { spawnSync } from "node:child_process"
import * as path from "node:path"

/**
 * Ralph loop guard enforcement for the OpenCode backend.
 *
 * Reuses the Claude PreToolUse guard scripts by synthesizing Claude-shaped
 * hook payloads from OpenCode tool arguments:
 *   bash        -> scripts/ralph-bash-guard.sh
 *   edit/write  -> scripts/ralph_protected_regions.py file-guard
 *                   (same entry point as scripts/ralph-file-guard.sh)
 *
 * The guards are active only when RALPH_LOOP=1, which ralph_loop.sh exports,
 * so interactive OpenCode sessions are unaffected. Unknown tool shapes fail
 * open (allowed) because the Python/shell guards cannot evaluate them; such
 * tools are a known residual gap, not a silent bypass of evaluated denials.
 * Serena MCP tool schemas are server-defined and therefore identical across
 * hosts, so their arguments are forwarded verbatim.
 */

const HOOK_TIMEOUT_MS = 10000

interface GuardResult {
  denied: boolean
  reason: string
}

/** Run a guard script with a Claude-shaped JSON payload on stdin. */
function runGuard(script: string, args: string[], payload: unknown): GuardResult {
  let proc: ReturnType<typeof spawnSync>
  try {
    proc = spawnSync(script, args, {
      input: JSON.stringify(payload),
      encoding: "utf8",
      timeout: HOOK_TIMEOUT_MS,
    })
  } catch {
    // Guard infrastructure failure: allow the tool call rather than wedging
    // the loop. The git-layer pre-commit checks remain as final defense.
    return { denied: false, reason: "" }
  }
  const stdout = typeof proc.stdout === "string" ? proc.stdout : ""
  if (!stdout.includes('"deny"')) return { denied: false, reason: "" }
  try {
    const reason = JSON.parse(stdout).hookSpecificOutput?.permissionDecisionReason
    return { denied: true, reason: typeof reason === "string" ? reason : stdout.trim() }
  } catch {
    return { denied: true, reason: stdout.trim() }
  }
}

function resolveRoot(ctxDirectory: unknown): string {
  if (typeof ctxDirectory === "string" && ctxDirectory.length > 0) return ctxDirectory
  return process.cwd()
}

function toAbsolute(root: string, p: unknown): string | null {
  if (typeof p !== "string" || p.length === 0) return null
  return path.isAbsolute(p) ? p : path.join(root, p)
}

const plugin: Plugin = async (ctx: any) => {
  const root = resolveRoot(ctx?.directory)
  const bashGuard = path.join(root, "scripts", "ralph-bash-guard.sh")
  const regionsScript = path.join(root, "scripts", "ralph_protected_regions.py")

  return {
    "tool.execute.before": async (input, output) => {
      const tool = input.tool
      const args = output.args ?? {}

      if (tool === "bash") {
        if (typeof args.command !== "string") return
        const res = runGuard(bashGuard, [], {
          tool_name: "Bash",
          tool_input: { command: args.command },
          cwd: root,
        })
        if (res.denied) throw new Error(res.reason)
        return
      }

      if (tool === "edit" || tool === "write") {
        const filePath = toAbsolute(root, args.filePath)
        if (filePath === null) return
        const toolInput: Record<string, unknown> = { file_path: filePath }
        let toolName = "Write"
        if (tool === "edit") {
          if (typeof args.oldString !== "string" || typeof args.newString !== "string") return
          toolName = "Edit"
          toolInput.old_string = args.oldString
          toolInput.new_string = args.newString
          toolInput.replace_all = args.replaceAll === true
        } else {
          if (typeof args.content !== "string") return
          toolInput.content = args.content
        }
        const res = runGuard("python3", [regionsScript, "file-guard"], {
          tool_name: toolName,
          tool_input: toolInput,
          cwd: root,
        })
        if (res.denied) throw new Error(res.reason)
        return
      }

      const lowerTool = tool.toLowerCase()

      // Multi-edit batches: forward when the edit list has a known shape.
      if (lowerTool === "multiedit" || lowerTool === "multi_edit") {
        if (!Array.isArray(args.edits)) return
        const edits: Record<string, unknown>[] = []
        for (const e of args.edits) {
          if (typeof e?.oldString !== "string" || typeof e?.newString !== "string") return
          edits.push({
            old_string: e.oldString,
            new_string: e.newString,
            replace_all: e.replaceAll === true,
          })
        }
        const res = runGuard("python3", [regionsScript, "file-guard"], {
          tool_name: "MultiEdit",
          tool_input: { edits },
          cwd: root,
        })
        if (res.denied) throw new Error(res.reason)
        return
      }

      // Notebook edits cannot be simulated; the guard denies them on
      // protected files and allows them elsewhere. Preserve that semantic.
      if (lowerTool === "notebookedit" || lowerTool === "notebook_edit") {
        const notebookPath = toAbsolute(root, args.notebookPath ?? args.filePath)
        if (notebookPath === null) return
        const res = runGuard("python3", [regionsScript, "file-guard"], {
          tool_name: "NotebookEdit",
          tool_input: { notebook_path: notebookPath },
          cwd: root,
        })
        if (res.denied) throw new Error(res.reason)
        return
      }

      // Serena MCP tools: strip the host-specific namespace prefix and
      // forward verbatim arguments; the guard knows the Serena shapes.
      // Unrecognized sub-tools fail open inside the guard (as on Claude).
      if (/serena/i.test(tool)) {
        const sub = tool.replace(/^(mcp__serena__|mcp_serena_|serena_)/i, "")
        const res = runGuard("python3", [regionsScript, "file-guard"], {
          tool_name: `mcp__serena__${sub}`,
          tool_input: args,
          cwd: root,
        })
        if (res.denied) throw new Error(res.reason)
        return
      }
    },
  }
}

export default plugin
