# Harness Research

Everything in this document was **verified empirically** against the named version by running a real agent, not inferred from documentation. Each claim carries the version it was checked against and the method used. Re-verify with `make conformance` after upgrading a harness.

Latest managed-project experiment: 2026-10-31 on macOS arm64; see the P0 record
below. Earlier harness findings were verified on 2026-08-15 against the versions
named in their sections, on Linux x86_64 and macOS arm64.

For per-agent quick reference (seam, modes, known limits), see the individual files in [`docs/harnesses/`](harnesses/). This document records the raw experimental findings that back those claims.

---

## Why this document exists

AgentReach intercepts agent harnesses at seams that are, in most cases, undocumented implementation details of closed binaries. A seam that silently changes shape is the single most likely way this project breaks for users. So every seam is (a) verified by experiment, (b) recorded here with its exact observed shape, and (c) covered by a conformance test that fails loudly when the shape changes.

The experiments here are not illustrations. They are the *source* of the claims made in the rest of the documentation. If a claim in the README or an architecture doc has no corresponding finding here, it is not a verified claim.

---

## Claude Code 2.1.233

### Distribution format

    # Linux x86_64 — verified on a clean install of this exact version
    $ file ~/.local/share/claude/versions/2.1.233
    ELF 64-bit LSB executable, x86-64, dynamically linked,
      interpreter /lib64/ld-linux-x86-64.so.2, not stripped       (310 MB)
    $ strings ... | grep -c _ZN2v8 ->  232   (V8 statically embedded)

    # macOS arm64, same version
    $ file ~/.local/share/claude/versions/2.1.233
    Mach-O 64-bit executable arm64                                (293 MB)
    $ strings ... | grep -c _ZN2v8 ->  0     (this build is stripped)

Claude Code ships as a **Node SEA (Single Executable Application)** with V8
statically embedded. It is not a `node script.js` invocation.

Sizes differ by platform, and the macOS build is stripped, so the V8 symbol
check that settles the question on Linux says nothing there. The consequence
below was therefore re-run as an experiment on macOS rather than inferred from
the Linux result.

Consequences, both verified:

| Technique | Works? | Evidence |
|---|---|---|
| `NODE_OPTIONS=--require preload.js` | **No** | Verified on Linux x86_64 and macOS arm64, against 2.1.233 on both: the preload never fired and never wrote its marker, while `claude --version` ran clean. Each run carried a control — the identical preload under a real `node` **does** fire — because a preload that silently failed to fire would "confirm" this for the wrong reason |
| `LD_PRELOAD` fs interposition | Not viable as a general strategy | Works for Claude (dynamically linked) but *not* for Codex, which is `static-pie` — no dynamic symbol interposition possible |

This kills in-process monkey-patching of `node:fs`. Native `Read`/`Edit`/`Write`/
`Grep`/`Glob` cannot be re-pointed from inside the process.

### `CLAUDE_CODE_SHELL`

Decompiled validation logic (extracted from the bundle's string table):

```js
async function isExecutable(e) {
  try { return fs.accessSync(e, constants.X_OK), true }
  catch (t) { let { code } = await run(e, ["--version"], { timeout: 1000 }); return code === 0 }
}
async function resolveShell() {
  let e = env.CLAUDE_CODE_SHELL;
  if (e)
    if ((e.includes("bash") || e.includes("zsh")) && await isExecutable(e))
      return log(`Using shell override: ${e}`), e;
    else
      log(`CLAUDE_CODE_SHELL="${e}" is not a valid bash/zsh path, falling back to detection`);
  // ... falls back to $SHELL, then /bin, /usr/bin, /usr/local/bin probing
}
```

**Rules, exactly:**

1. The *path string* must contain the substring `bash` or `zsh` anywhere — not
   just the basename. `/opt/reach/reach-bash` qualifies.
2. It must be executable (`X_OK`), or exit 0 on `--version`.
3. On failure it falls back silently to shell detection — the agent keeps
   working against the **local** machine. A misconfigured shim is therefore a
   silent correctness hazard, which is why `reach doctor` checks the name rule.

Invocation shape, observed:

    argv = ["-c", "<command>"]
    argv = ["-c", "-l", "<snapshot-builder-script>"]   # once per session

### `CLAUDE_CODE_SHELL_PREFIX`  ← the seam reach uses

Observed invocation:

    ARGC=1
    ARGV[0]=<<<source /home/ubuntu/.claude/shell-snapshots/snapshot-bash-1786757311642-4kz5ln.sh 2>/dev/null || true && { shopt -u extglob || setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; } >/dev/null 2>&1 || true && { \builtin unalias -- 'unsetenv'; \builtin unset -f -- 'unsetenv'; } >/dev/null 2>&1 || true && eval 'echo PREFIX_MARKER_991' < /dev/null && pwd -P >| /tmp/claude-1003-cwd>>>

The prefix program receives the **entire command envelope as a single argv
element**. No `-c`, no name validation, and the prelude becomes shell-agnostic
when the variable is set:

```js
function prelude(shellPath) {
  if (env.CLAUDE_CODE_SHELL_PREFIX)
    return "{ shopt -u extglob || setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; } >/dev/null 2>&1 || true";
  if (shellPath.includes("bash")) return "shopt -u extglob 2>/dev/null || true";
  if (shellPath.includes("zsh"))  return "setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL 2>/dev/null || true";
  return null;
}
```

That the prelude is deliberately written to work under *either* shell is strong
evidence this variable exists for wrapping execution in a foreign environment
(container/remote), which is exactly reach's use case. This is the seam reach
targets: one argument, no parsing of flags, stable contract.

### The command envelope

Every Bash tool call arrives wrapped:

    source <LOCAL_SNAPSHOT>.sh 2>/dev/null || true
      && { shopt -u extglob || setopt ... ; } >/dev/null 2>&1 || true
      && { \builtin unalias -- 'unsetenv'; \builtin unset -f -- 'unsetenv'; } >/dev/null 2>&1 || true
      && eval '<USER COMMAND>' < /dev/null
      && pwd -P >| /tmp/claude-<rand>-cwd

Three parts matter to reach:

**1. The snapshot (`source ...`)** — a file generated on the *local* machine
capturing the user's shell functions, aliases, `shopt`, and `PATH`. reach
**strips** this segment rather than forwarding it, for two reasons:

- It references local absolute paths that do not exist remotely; sourcing it
  remotely is a silent no-op at best.
- It leaks the local username and directory layout to the remote host. On an
  untrusted client server that is an information disclosure reach must not
  cause. See `docs/SECURITY.md`.

**2. `pwd -P >| /tmp/claude-<rand>-cwd`** — this is how Claude Code persists
`cd` between Bash calls. The path is **regenerated per invocation** (observed
`/tmp/claude-1003-cwd` then `/tmp/claude-a4c6-cwd`), and Claude Code reads it
back on the local filesystem after the command returns.

If the envelope is forwarded verbatim to a remote host, this file is written
*remotely* and the local read finds nothing — **`cd` silently stops
persisting**. reach therefore strips this segment, tracks cwd itself, and
writes the resolved working directory to the local path the envelope named.
This is the single most important correctness detail in the Claude Code
adapter.

**3. Embedded tool shadowing** — the snapshot installs shell functions that
replace `rg`, `find`, and `grep` with Claude Code's *embedded* binaries:

```bash
function rg   { (exec -a rg    "$CLAUDE_CODE_EXECPATH" "$@") }
function find { (exec -a bfs   "$CLAUDE_CODE_EXECPATH" -S dfs -regextype findutils-default "$@") }
function grep { (exec -a ugrep "$CLAUDE_CODE_EXECPATH" -G --ignore-files --hidden -I ... "$@") }
```

Because reach strips the snapshot, these functions are never defined remotely
and `rg`/`find`/`grep` resolve to the remote host's real binaries. That is the
desired behaviour — but it means **remote hosts without ripgrep get plain
grep semantics**, which `reach doctor` reports.

Stripping the snapshot leaves a gap that had to be filled separately. The
snapshot is also what carries the operator's `PATH`, and `ssh host command` runs
a *non-interactive* shell, which on Debian and Ubuntu returns from `~/.bashrc`
before reaching anything that edits it. Measured on a real host, the two PATHs
differed by five directories: the login shell had `~/.local/bin`, `~/bin`,
`~/.cargo/bin`, `~/.deno/bin` and a toolchain directory that a plain `ssh
host command` never sees.

That matters because `cargo install ripgrep` is how most people get `rg`. reach
would have reported "no ripgrep on the target" and fallen back to grep on a
machine that has it. reach therefore asks the login shell for its `PATH` once
during `reach up`, and applies it to detection and execution alike — finding a
tool it could not then run would be worse than not finding it.

### Native file tools

`Read`, `Edit`, `Write`, `Grep`, `Glob` do **not** route through any shell and
have no environment-variable seam. With MCP excluded by design and in-process
patching impossible (SEA), the only way to make them act on remote content is
to place real files at the paths they read. See "Materialisation" in
`ARCHITECTURE.md`.

---

## Codex CLI 0.147.0

Distribution, Linux x86_64: `ELF 64-bit LSB pie executable, static-pie linked,
stripped` (246 MB), Rust. On macOS arm64 the same version is a 210 MB Mach-O.

`static-pie` means **no dynamic linking**, so `LD_PRELOAD`/`DYLD_INSERT_LIBRARIES`
interposition is impossible by construction. Checked rather than inferred from
the label: the binary declares **no ELF interpreter and zero `NEEDED` entries**,
so there is no dynamic loader to interpose on. This is the reason the Codex
adapter is a `PATH` shim rather than a library.

Relevant configuration surface found in the binary: `unified_exec`,
`shell_command`, `shell_type`, `danger_full_access`, `workspace_write`,
`read_only`, `apply_patch`. Hook events exist but — per upstream docs and
issue #18491 — `PreToolUse` fires for the shell tool only and the only decision
Codex acts on is `deny`; `updatedInput` is parsed but rejected. So hooks cannot
rewrite a command, and the adapter uses shell-level substitution instead.

See `docs/harnesses/codex.md`.

### Codex CLI 0.148.0 (update)

The seam above is gone in 0.148.0. Session rollouts record the shell tool's
argv as `["/bin/zsh", "-lc", …]` — an absolute path. The tagged source
(`codex-rs/shell-command/src/shell_detect.rs`) resolves the login shell via
`getpwuid_r` and only falls back to `which` (PATH) when the account shell is
missing or unrecognised, so the PATH shim never runs. No config key, env var,
or hook changes this (`zsh_path` is internal to the under-development zsh-fork
backend; `PreToolUse` remains deny-only; `wire_api = "chat"` was removed, which
is why the offline probe speaks the Responses API). The macOS build is
hardened-runtime signed without `disable-library-validation`, so
`DYLD_INSERT_LIBRARIES` is blocked as well. reach therefore measures the seam
(`reach harness verify codex`) and refuses versions that bypass it.

## Kimi Code CLI 0.34.0

Distribution: 169 MB Mach-O on macOS arm64 (Bun-compiled TypeScript), MIT;
164 MB dynamically linked ELF on Linux x86_64 at 0.31.1.

Confirmed present in the binary: `PreToolUse`, `PostToolUse`, `plugin`.
Supports `-p/--prompt` and `--output-format stream-json`, which makes it
scriptable for E2E testing.

Re-checked against 0.34.0 after the version this was first written for (0.31.1)
went stale: all four still hold. That re-check is the reason this document
carries versions at all — a claim about a closed binary is a claim about one
build of it.

Re-checked again against 0.37.2 and the seam claim no longer holds: the Bash
tool spawns its shell by absolute path, so the PATH shim is never invoked. The
documented hooks honour allow/deny only and cannot rewrite a tool call's
input. Measured by `reach harness verify kimi`, which drives 0.37.2 against an
offline mock over the chat-completions wire (`KIMI_MODEL_*` env vars point it
at the mock) and watches a scripted command run on the local machine.

See `docs/harnesses/kimi.md`.

## Grok Build 1.0.5

Verified on macOS arm64 against `grok 1.0.5 (5115b46bc909) [stable]`.

### `$SHELL` is the command seam

A logging wrapper was installed as `$SHELL` and Grok was asked, via
`--always-approve --tools run_terminal_command -p`, to run `echo SHELL_PROBE`.
The wrapper was invoked. The PATH shim (`PATH=<shim>:$PATH` with `$SHELL`
left at `/bin/zsh`) was **not** invoked. `GROK_SHELL` is present in
`xai_grok_config::shell` in the binary; the live tool path followed `$SHELL`.

### Invocation shape

Environment snapshot (stays local):

    argv = ["-lc", "source \"$HOME/.bashrc\" … builtin alias -p …"]

Tool command (payload after `--` is the model command):

    argv = ["-O", "extglob", "-c", "<envelope using __grok_user_cmd=\"$1\">", "--", "<command>"]

The envelope evals a snapshot from fd 3, then `eval "$__grok_user_cmd"`.
reach unwraps the `--` payload rather than forwarding the envelope.

### File tools

`read_file`, `search_replace`, `list_dir`, `grep`, `write` act on the local
disk. `write` is advertised in a live session and appears in no documentation.

Permission rules are the wrong instrument. Measured one rule at a time, with a
shell command that only the rule under test could block:

| rule | denies native tool | also denies in the shell |
|---|---|---|
| `Read` | `read_file`, `list_dir` | `cat` |
| `Write` | `write` | `echo > file` |
| `Edit` | `search_replace` | `sed -i` |
| `Grep` | `grep` | — |

Grok classifies a shell command that touches a file under the same prefix as
the tool, so denying the tools denies the shell workflow that replaces them.
The tools are removed instead with an agent profile's `disallowedTools`, which
leaves the shell untouched.

`--agent` pointing at a missing file does not error; grok falls back to the
default agent with every local file tool enabled, silently.

Subagents disabled.

See `docs/harnesses/grok.md`.

## opencode

Not installed on the verification machine at time of writing; adapter is built
against the documented plugin API (custom tools whose filename matches a
built-in take precedence, plus `tool.execute.before`/`after` hooks). Marked
**unverified** in `docs/harnesses/opencode.md` until an E2E run exists.

---

## Reproducing these results

    make conformance          # runs the probes below against installed harnesses

The probe rig works by pointing the harness's shell seam at a logging shim and
running a real prompt through the agent, then asserting on the captured argv.
Note that a nested `claude` run inherits `CLAUDECODE=1`,
`CLAUDE_CODE_CHILD_SESSION=1` and a possibly-invalid `ANTHROPIC_API_KEY` from
its parent; all of these must be unset or the run hangs or fails
authentication. `clean_agent_env` in `test/e2e/lib.sh` does this.

## Managed OrbStack P0 — 2026-10-31

This experiment covers P0 only. It does not establish the later workspace-policy,
Host capability, persistent-terminal, service/port or reconciliation features.
The native control plane was macOS arm64, OrbStack 2.2.4 and Docker CLI/Engine
29.8.2. The dedicated managed fixture used UID/GID `10001:10001`, Guest workdir
`/workspace/probe`, shared-project mode, restricted container security and one
explicit external bind. Its initial container ID began `670140ac13d8`; its
initial `StartedAt` was `2026-10-31T03:13:42.573944362Z`. Native volume checks
independently matched external exFAT source and APFS engine volumes. Their
machine-specific paths, UUIDs and project JSON are kept in local acceptance
evidence, not copied into this repository.

All Go development, formatting, dependencies, tests and builds ran in the Linux
arm64 development container, Go 1.25.8 with `GOTOOLCHAIN=local`. macOS acceptance
ran its `CGO_ENABLED=0` cross-built binaries; no Host Go installation was used.
The separately installed lint executable was golangci-lint v2.12.2.

### Runtime and persistent channels

The initial native candidate SHA-256 was
`e504f20f0f2b0147e39edaf31dabb5314cc047a819b457de328af6d0248239ea`.
It registered the existing approved fixture, ran Guest `hostname`/`id`/`pwd`, and
wrote/read unique Guest `/tmp` canaries whose corresponding Host `/tmp` paths
were absent. A process-local `DOCKER_CONTEXT=default` override returned
`context_mismatch` before either Host or Guest command canary was created.

Native exec-server JSON-RPC, on that same candidate, completed initialize and
ready status; `process/start` plus piped `process/write` returned `stdin:hello`
and exit 0; `fs/writeFile` and `fs/readFile` round-tripped the file. Managed
`tty=true` and `process/signal` returned explicit unsupported errors. This is not
PTY support, and transport termination does not guarantee Guest process death.

On one RPC connection after a file helper pool and a waiting stdin process
already existed, temporarily removing only the fixture's source-volume sentinel
made the next `fs/readFile`, `process/write` and `environment/status` return
`storage_unavailable`. The old pipe closed and neither Host nor Guest stale-stdin
canary appeared. The sentinel was restored and its original SHA-256 rechecked.
This tests next-dispatch rejection, not instantaneous revocation of arbitrary
I/O that was already executing. Original frames are in the local
`rpc-e504f20f.log` acceptance artifact.

### Cwd and native patch diagnosis

An earlier shell probe containing `apply_patch <<...` followed by `test` and a
hostname canary failed. Native audit evidence showed that its command contained
no injected `cd`, but its requested cwd retained a Host temporary directory.
The Host `/var/...` workspace alias and Codex's canonical `/private/var/...`
spelling had not both been mapped. The final server accepts both protocol path
spellings and maps them to the Guest root without changing the shell command.

The same experiment exposed a transport bug: `cd missing && export ...; body`
could still execute `body`. A command with several statements had the same
failure even without an explicit environment. `BuildCommand` now checks cwd
with `cd ... || exit $?` before any exports or body. Real local POSIX-shell
regressions cover missing cwd with zero, one and two env keys, assert that no
file side effect occurs, and preserve successful heredoc/env/exit semantics.

Source diagnosis used the official fixed
[`rust-v0.148.0` apply-patch handler](https://github.com/openai/codex/blob/rust-v0.148.0/codex-rs/core/src/tools/handlers/apply_patch.rs)
and parser: native custom `apply_patch` verifies/updates the selected executor
filesystem; only recognized patch-only shell forms are intercepted. The compound
shell prefix above was not proof of that native tool, and the Guest had no
standalone `apply_patch` executable. No readiness-time helper installation or
command-text routing workaround was added.

`reach-mock` has fallback metadata with no patch tool. The fixed binary's bundled
[`models.json`](https://github.com/openai/codex/blob/rust-v0.148.0/codex-rs/models-manager/models.json)
contains `gpt-5.4` with freeform patch metadata, so the final offline helper uses
that label to advertise the native custom tool. Requests still go only to its
localhost mock and consume zero real model tokens. An unadvertised patch tool
fails the test; it is never replaced with a shell approximation.

### Real launcher, Claude callback and account home

The final native reach candidate SHA-256 was
`f1e437d841e6d4a504d87ccb307132fbdf4d05d56de4d10ba3c4117f7d3ebb68`.
The cross-built managed-launch helper SHA-256 was
`be5a6751fbe701db2f9b298cafc3ae7f1e6ba8e10518b80b84fe768b34b27d3e`.
Actual installed binaries exercised were:

| Harness | Version | Real executable SHA-256 |
| --- | --- | --- |
| Codex | 0.148.0 | `b0308517b20543012fa2171aa3d46ce455a7456c4eb2a552ab9468ba4eeb1e50` |
| Claude Code | 2.1.220 | `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081` |

[`test/conformance/managed`](../test/conformance/managed/main.go) invoked the
actual `reach --project ... codex|claude` launchers. Each launcher first ran its
fresh strict readiness probe, then replaced its process with the selected
harness. The helper supplied fresh temporary HOME, empty source CODEX_HOME,
fictitious API keys, an explicit small env and localhost mocks. Codex's test-only
PATH executable prepended offline provider configuration without extending the
managed argument allowlist. Its readiness fingerprint measured that executable;
the helper separately hashed the real Codex binary, as shown above.

Final clean logs `launcher-codex-sol3-final-shell.log`,
`launcher-codex-sol3-final-patch.log` and
`launcher-claude-sol3-final-shell.log` all passed. Both shell paths checked cwd
`/workspace/probe`, Guest file write/read, target hostname `670140ac13d8` and
absence of the fictitious Host-only env key inside the Guest command. The patch
path issued a native `custom_tool_call`, added a relative file, then independently
read its exact contents through `reach --project ... exec`. The corresponding
Host control cwd file and Host Guest-absolute-path spelling were both absent.
The shared bind's approved Host source naturally contains the Guest file; that
is expected, not evidence of an unmediated Host file operation.

Claude's final-launch test initially failed despite a successful readiness
probe. The readiness probe uses `--dangerously-skip-permissions` for its isolated
headless run; the production managed launcher does not. Isolated diagnostics
established that the configured PreToolUse callback actually reached the prefix,
with raw command naming the real reach and a valid hook environment. On macOS,
`os.Executable` in the prefix process instead named its symlink, so the full
registered command comparison failed and the typed hook never returned allow.
Registration/checks now use the same resolved executable via `selfPath`, retaining
exact command matching and typed dispatch. A direct-versus-symlink subprocess
regression was added. With that minimal fix, the identical shell expansion
command passed the actual launcher with a parsed typed-hook `allow`; no generic
Host shell, broadened callback parser or production permission bypass was added.
All temporary diagnostics were removed before the final candidate was built.

Every dispatch now also checks fixed `/usr/bin/dscacheutil` output for the actual
UID against the approved canonical `control_home`. Native acceptance used a
temporary harness HOME while preserving the project's real native account home;
the checks and launcher still passed. Missing, duplicate or mismatched UID/home
records are rejected in injected tests before any Docker subprocess.

The empty fixture lacks `.git`, so Codex logged an AGENTS.md discovery warning;
its requested tools, readback and canary assertions still completed successfully.
This is offline headless conformance with the named binaries, not paid-model,
interactive TUI or general credential-flow proof. Explicit exec-server env maps
remain explicit inputs; the fictitious env test measures the actual harness
shape tested rather than proving that every possible env map is harmless.

### Final Guest checks and remaining acceptance

For the final clean source, `make check` completed vet and race tests. A separate
JSON race-test run recorded **567 passing test/subtest entries, 0 failures and 4
platform-condition skips**, with **15 passing packages and 2 packages without
tests**. The skipped tests were `TestMapEmbeddedCwdHandlesPrivateTmp` (macOS) and
`TestSetPathEnvPreservesTheKeySpelling`, `TestSameDirIsCaseInsensitiveOnWindows`,
`TestProgramBaseStripsTheWindowsExtension` (Windows). Skips are not native-platform
passes. Linux and Darwin lint each reported 0 issues. Linux/arm64, Darwin/arm64
and Windows/amd64 `./cmd/...` builds passed in the Guest. Native Windows tests,
all hosted CI jobs and physical unplug/engine restart/LAN experiments were not
executed.

Guest evidence files are `/tmp/agentreach-p0-sol3-final-check.log`,
`/tmp/agentreach-p0-sol3-final-tests.json`,
`/tmp/agentreach-p0-sol3-final-lint.log`,
`/tmp/agentreach-p0-sol3-final-darwin-lint.log` and
`/tmp/agentreach-p0-sol3-final-build-sha256.txt`. The live native evidence and
actual private project JSON are held separately under the task's temporary
acceptance directory. Independent audit and the dedicated fixture's lifecycle
negative tests are separate acceptance gates; their results must be recorded
before marking P0 complete.

### Independent-audit corrections — 2026-11-01

The first independent review blocked the earlier candidate on the boundaries
below. This section records the implementer's corrections and regression
results; the narrowly scoped independent re-review and phase commit remain
separate gates. No P1-or-later feature or commit is included here.

| Audit item | Correction and regression evidence |
| --- | --- |
| A: parser/session disagreement | Unverified harnesses and `env` reject the actual parsed Session before lookup, version execution, shim installation or export. Tests cover `-session`, `--session`, equals syntax, the harness `--` boundary, positional `env`, legacy `env`, and session-independent `opencode uninstall`. |
| B: Host identity and control paths | Overlap and ancestry use `os.SameFile`, including missing control suffixes, case aliases and hard-link aliases. Reserved credential components are compared as actual filesystem objects instead of globally lowercased strings. Existing control ancestors/leaves reject symlinks; registered project files, session/settings/auth leaves and sentinels reject shared hard links. Linux regressions cover these boundaries and preserve a distinct uppercase object on a case-sensitive filesystem. Native external-volume case-alias registration is refused before creating control state. |
| C: first executable/version boundary | Unsupported harnesses are rejected before version execution. Both lookup alias and resolved executable must be outside writable Guest binds; the resolved executable is hashed before `--version`, after version execution and after the fresh probe. Version, probe and launch use trusted cwd and cleaned environment/PATH. Managed probe HOME/cwd is created under the verified control root, independent of ambient `TMPDIR`. A real Guest-created executable selected through relative PATH is refused without running its version command. |
| D: persistent-channel invalidation | Any exec-server guard failure permanently retires its connection, closes stdin/transport streams and closes the shared file pool without holding server/process locks during I/O cleanup. Status/info/start/file regressions cover failure, guard restoration, rejection of old stdin and file handles, cleanup, and a fresh working connection. Native status-first failure/restoration also passes. |
| E: missing fixed container ID | Only the measured C-locale `[]` plus exact `Error response from daemon: No such container: <full ID>` inspect result becomes `identity_changed` with explicit registration guidance. Connection and other/ambiguous errors retain their cause. Unit tests exercise both with no execution; native inspection of the previously deleted fixture ID reports the corrected category. |
| F: Windows state persistence | File flushing and same-directory replacement are retained. Unix directory-sync failures still propagate. Windows no longer attempts `FlushFileBuffers` through a read-only directory handle and does not promise directory-entry durability after power loss. POSIX-only executable fixtures are explicitly scoped; project-record fixtures use native absolute paths. |
| G: Claude Host cwd bookkeeping | Managed bookkeeping accepts only a direct system-temp `claude-<hex>-cwd` leaf. Validation occurs before Guest dispatch; `os.Root` anchors operations and a fresh exclusive temporary file is renamed over the leaf, so symlinks are not followed and hard-link targets are not truncated. Unit tests include a replacement race. Native explicit-interface tests reject arbitrary/symlink destinations with no Guest touch and no dummy Host-file change. This is an interface regression, not evidence that a model exploited the real Claude-generated envelope. |
| H: child and live-session binding | Managed children carry fixed expected project/record digests, including in Codex executor configuration. Session loads reject downgrade/rebinding in that context. Live checks reread the persisted session association, including target/mode/creation identity. A new registered binding uses an explicit bootstrap guard before its first session save. Validated prefix dispatch reuses its loaded Session. Unit and native `down`/same-name `local` reconstruction regressions refuse the old managed exec-server; status-first removal/restoration also retires existing channels. |
| I: harness command grammar | Claude accepts at most one print prompt and constrained model names, rejecting `cc://`/`cc+unix://` routing. Codex accepts interactive mode or one-prompt `exec`, excluding `resume`/`review`. Unit regressions cover management-command shapes; the previously measured real Claude URI/MCP argument combination is now rejected by reach before launch. |
| J: nested mount identity | Native `statfs` for every used sentinel/engine/bind path must match its verified claim's filesystem identity and mountpoint. Injected nested-filesystem tests reject before any Docker call; no real mount changes were made. |
| K: dangling session entries | Session Load first uses `Lstat`; a dangling link or other non-regular directory entry is corruption, not an empty slot. Save preserves that evidence. |
| L: strict probe success/cwd | Managed probes require successful process exit and the measured canary. Strict Codex no longer installs a legacy shell-shim/workspace fallback. Claude checks the persisted current Guest cwd; Codex checks the mapped workspace. Native Claude startup from persisted `/tmp` succeeds, and a real Codex wrapper returning the correct canary before exiting 7 is refused. |

One full Guest `make check` completed vet and race tests with **16 passing
packages and 2 packages without tests**. Linux and Darwin lint reported **0
issues**; Linux/arm64, Darwin/arm64 and Windows/amd64 command builds passed.
Subsequent changes only to the trusted probe HOME and B's control-leaf identity
checks were verified with race tests/lint for their affected packages and rebuilt
Darwin/Windows outputs; unrelated packages were not rerun. The conformance
helper's new `--guest-cwd` option received its own vet/lint/build checks. Final
`gofmt -l` and `git diff --check` were clean. All Go operations remained in the
same Linux Go 1.25.8 development Guest. Native Windows execution and hosted CI
were not performed; the cross-build is not a native Windows test result.

Final Darwin reach SHA-256:
`e2c7ef6f11b6e09aa500da00e550fa2889d8a3bb892a83e06a42decb4d9a3ecf`.
Final managed-conformance helper SHA-256:
`39c8c78132ae26cba043de7fdf97e24e9ba9286e0dc36847d29148bcb65f48b2`.
The real Claude 2.1.220 and Codex 0.148.0 hashes remain those recorded above.
The final candidate registered a fresh isolated state and passed actual Claude
shell, Codex shell, native custom Codex patch, and non-root Claude launch tests.
Each successful shell test retained the Guest cwd/read/write/hostname and dummy
Host-env-canary assertions. The native patch still received its advertised
custom-tool result, independent Guest readback and absent Host control-cwd file.
The exit-7 negative log contains the observed marker/target hostname followed by
reach's strict refusal. No request reached a paid model service.

Guest evidence is `/tmp/agentreach-p0-audit-check.log`,
`/tmp/agentreach-p0-audit-lint.log`,
`/tmp/agentreach-p0-audit-darwin-lint.log`,
`/tmp/agentreach-p0-audit-trusted-home-{check,lint}.log`,
`/tmp/agentreach-p0-audit-private-leaf-{check,lint}.log`,
`/tmp/agentreach-p0-audit-project-identity-{check,lint}.log`, and
`/tmp/agentreach-p0-audit-build-sha256.txt`.
Final native evidence under the existing isolated acceptance directory is
`audit-delivery-{register,claude-shell,codex-shell,codex-patch,claude-nonroot,observed-probe-exit7,regressions,cwd-boundary}.log`.
Earlier lifecycle logs `lifecycle-{sol3,name-binding}.{md,log}` remain valid for
their recorded candidate and distinguish stopped/restarted/replaced containers.
This correction round did not alter container/engine/volume lifecycle or global
Docker context. Its temporary waiting RPC clients exited after EOF, its test
wrappers/symlinks were removed, and its random Guest canaries were retained as
evidence. The final new isolated native state is
`/private/tmp/agentreach-p0-host/state-audit-e2c7ef6f`; the previously restored
probe state was not changed.

### Remaining D/F review corrections — 2026-11-05

The bounded re-review found one remaining D path: a managed transport's own
check still called `Session.CheckManaged` directly, so a failure first observed
inside `ContainerTransport.Open` did not set the server's permanent failure.
Exec-server now constructs its managed transport with `TransportGuard` and the
same `runtimeReady` callback used by its FileOps. Constructor state is initialized
once and populated in place; it no longer overwrites the guard's failure state.
Managed initialization failures close their available transport/resources.

`TestTransportFirstGuardFailureRetiresExistingServer` uses a real piped
container-transport fixture with an injected guard. After warming a file pool,
file handle and stdin process, the outer check succeeds and the second check
inside transport Open fails. The condition then recovers. The old server still
rejects environment readiness/info, process start/write, its old file handle,
direct pool reads and transport Open, with no additional runtime dispatch or
canary side effect; cleanup remains available and a newly constructed server
works. This specifically covers the path missed by the earlier status-first
regression.

F's remaining issue was fixture spelling: legitimate temporary control/workspace
roots are now resolved before use in session, project, harness, runtime and
identity tests. Deliberately introduced symlink/hard-link aliases remain intact
in their negative tests. A run with `TMPDIR` itself set to a temporary symlink
passes all eight affected test packages under the race detector. The synthetic
alias uses a short `/tmp/ar-...` parent so the socket fixture stays within Unix
socket address-length limits; a preliminary longer parent exceeded that kernel
limit and was not treated as a product failure or a successful alias run.

Normal affected-package race tests passed, followed by the symlink-TMPDIR run;
Linux and Darwin lint both reported **0 issues**. Linux/arm64, Darwin/arm64 and
Windows/amd64 command builds passed in the existing **Go 1.25.8 linux/arm64**
development Guest. Final formatting and diff whitespace checks were clean.
Unchanged full-suite and earlier native A–L evidence were retained rather than
repeated. Native Windows execution remains unperformed.

The new Darwin reach candidate supersedes the November 1 candidate above:
`0376bd39f2a860d22475aa42d0f4ec59bacb78b8c0da157e3e06711cb6b3ae1e`.
Its managed-conformance helper is
`1edb520efdff76a59e5e964aac727a826d1ef5a419b3d8d219637198c9928acb`.
Using the existing isolated approved state, native managed exec, actual Codex
shell, native custom Codex patch, and actual Claude shell all passed. These runs
used localhost mocks and dummy credentials and did not alter runtime lifecycle.

Guest evidence is `/tmp/agentreach-p0-final-df-check.log`,
`/tmp/agentreach-p0-final-df-execserver-check.log`,
`/tmp/agentreach-p0-final-df-tempalias.log`,
`/tmp/agentreach-p0-final-df-lint.log`, and
`/tmp/agentreach-p0-final-df-build-sha256.txt`.
Native evidence is in the existing acceptance directory as
`final-df-{exec,codex-shell,codex-patch,claude-shell}.log`; the actual launcher
logs contain `PASS actual_launcher=true`. The implementer has frozen the source
for the remaining bounded D/F independent re-review; no phase commit or later
phase implementation is included in this correction round.

The final F fixture follow-up also includes `internal/harnessprobe`: the trusted
and Guest roots in `TestManagedVersionUsesTrustedCwdAndCleanEnvironment` are
resolved before its shell compares `$PWD`. That package's symlink-TMPDIR race
run passed, and Linux/Darwin lint each reported 0 issues. Evidence is
`/tmp/agentreach-p0-final-df-harnessprobe-tempalias.log` and
`/tmp/agentreach-p0-final-df-harnessprobe-lint.log`. This follow-up changes only
test fixture paths and this record; the `0376bd39...` product candidate is
unchanged and was not rebuilt or relaunched.
