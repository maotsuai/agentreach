# Codex

Works against **0.148.0**, through a seam Codex itself provides: its
exec-server protocol. Re-verify any version with `reach harness verify codex`.

For managed OrbStack projects, see [managed paths](../MANAGED-PROJECTS.md#managed-paths-and-command-cwd).
The rooted helper is required and URI mapping occurs once before Guest FileOps.
Shared Host aliases and original/canonical control cwd aliases cannot be strict
component ancestors or descendants of the Guest root; equality is allowed.
Ambiguity returns `path_denied` before dispatch without migrating or rebinding
records. The managed profile refuses local filesystem/configuration overrides.

Managed P2 keeps `include_local=false`. Codex 0.148.0 local stdio MCP requires
a local executor, so the fixed MCP server selects `reach`: only its exact trusted
binary/argv/cwd/env binding enters an in-process Host MCP handler. Ordinary argv
still starts in the Guest. MCP JSON lines bypass the Shell sentinel tail buffer.
The three fixed tools use reach policy for approval; operator approve/revoke
remain unavailable to models. See [POLICY.md](../POLICY.md).

## The finding that shapes everything

The original 0.148.0 wire probe advertised `exec_command`, `write_stdin` and
five tools without filesystem access, with patching routed through the command
tool. Tool advertisements also depend on bundled model metadata: the P1 offline
conformance profile advertises native custom `apply_patch`. That actual tool
has been verified through the exec-server filesystem for add/update/delete/move
and outside-root rejection; it is not a compound shell-patch approximation.
The exact profile and evidence are recorded in [P1 validation](../validation/P1.md).

So Codex's entire tool surface funnels through one protocol. Intercept that
protocol and there is nothing left to deny, nothing to mirror, and no gap for
a wrong-machine write to slip through. This is the cleanest fit reach has —
cleaner than Claude Code, where whole tool families must be denied or mirrored
around the fact that they cannot be redirected.

## The seam

Codex 0.148 supports *remote environments*: `$CODEX_HOME/environments.toml`
points it at an external exec-server, a JSON-RPC endpoint that receives every
process spawn and filesystem operation. reach is that server. `reach codex`
builds a controlled `CODEX_HOME` — the operator's real one is never touched —
whose `environments.toml` selects reach and sets `include_local = false`, so
the local machine is not merely avoided, it is absent from the list.

Every `process/*` call runs on the target through the session transport; every
`fs/*` call is served from the target through reach's fileops. If the session
is broken the server fails loudly. There is no code path that falls back to
local execution, because the local machine is not a value the protocol can
name.

The launch guard is unchanged in shape: `reach harness verify codex` drives
one real turn against an offline mock model — no API key, no tokens — and
checks where the scripted command actually ran. Because old verdicts measured
a different seam, the verdict cache is schema-versioned and discards them
rather than trusting them in either direction.

## The lesson the PATH shim taught

This document once claimed the PATH shim worked against Codex ≤ 0.147. It did
not, and the error is worth recording because of *why* it survived.

The conformance check probed `codex sandbox -- bash -c …`. That resolves the
*user-supplied* program through `PATH`, so it stayed green no matter what the
shell tool itself did. Meanwhile codex had been resolving the login shell
through the account database (`getpwuid_r`) and spawning it by absolute path —
invisible to `PATH` — since before the code was split out of `codex-core` in
February 2026. Behavioural re-measurement in August 2026 found the shim
bypassed on every version probed: 0.140.0, 0.146.1, 0.147.0, 0.148.0.

Two design rules came out of that, and they now apply to every harness:

- **A claim about a harness needs an experiment that cannot lie.** Reading
  docs, reading source, and probing adjacent code paths all lie comfortably.
  The probe drives a real turn and checks which machine answered.
- **Prefer the harness's own remote-execution design over intercepting its
  process.** A shim fights the binary; a protocol cooperates with it. When
  codex grew a supported seam, reach moved to it and deleted a sandbox
  workaround, a network exemption, and a class of version fragility in one
  motion.

## Not the seam

Hooks — Codex's `PreToolUse` fires for the shell tool only, and the only
decision it honours is `deny`; `updatedInput` is parsed and rejected (upstream
issue #18491). Config — `ConfigToml` has no shell-path key; `zsh_path` is
internal to the under-development zsh-fork backend. Interposition — the macOS
binary is hardened-runtime signed without
`com.apple.security.cs.disable-library-validation`, so
`DYLD_INSERT_LIBRARIES` is unavailable. Each of these was checked, not
assumed; each is a dead end; the exec-server environment is the seam.
