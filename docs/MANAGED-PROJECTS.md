# Managed OrbStack projects (P0 + P1 + P2)

Managed projects add an explicit Host authorization around an **existing**
OrbStack Docker container. Ordinary SSH, local, Docker and Podman sessions keep
their existing behavior. P0 never pulls, builds, creates, starts, restarts or
automatically rebinds a runtime. `runtime health` means inspect and verify.

The control plane runs on macOS. Linux tests inject volume inspectors; a Linux
binary refuses native OrbStack volume checks. Guest commands and file operations
execute through Docker, including requests over already-open file helper pools
and exec-server stdin channels.

## Prepare the managed helper explicitly

P1 requires a Linux helper at the fixed Guest path
`/usr/local/bin/reach-helper`, for both workspace modes. Prepare it before
registering a new project. Build inside the Linux development Guest with the
repository's Go 1.25.8 toolchain and the target architecture:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags '-s -w -X main.version=0.8.0' \
  -o /approved/output/reach-helper-linux ./cmd/reach-helper
sha256sum /approved/output/reach-helper-linux
```

The [Dockerfile](../examples/orbstack/Dockerfile) expects that explicitly built
`reach-helper-linux` in its build context and copies it with mode 0755. Building
or provisioning an image/container remains an operator action. Verify the actual
Guest file is executable by the registered UID and run its `--selftest`, comparing
the digest with the Guest-built artifact. Host external filesystems such as exFAT
may not preserve Unix modes/ownership through a plain copy; the image COPY mode
or explicitly preserved archive metadata must produce the required Guest mode. A missing helper,
a P0 helper (even with the same release version), or an incompatible rooted
capability returns `unsupported` with preparation instructions. Managed reads,
writes and command starts never build a helper on the Host, upload one, install
one, or fall back to the POSIX/Python tiers. Unmanaged tier negotiation and its
explicit helper installation keep their existing behavior.

Existing P0 project/session records remain readable and their immutable runtime
bindings remain enforced. Their Guest must be prepared explicitly with the new
helper before managed use. An existing session cannot bypass this requirement.
If preparing a new image recreates/restarts the container, its old binding stays
invalid; register a new project ID rather than overwriting the old record.
Helper self-identification and capability negotiation do not establish protection
against a malicious Guest of the same UID replacing the helper or controlling
its kernel.

## Register an existing container

Start from [the JSON example](../examples/orbstack/project.example.json). This is
the implemented format; the blueprint's conceptual TOML is not accepted. All
fields are strict: unknown or duplicate keys, damaged records and unsupported
schemas fail. Replace every placeholder and use the **resolved absolute** Docker
executable, its explicit config directory, `orbstack` context, approved Unix
endpoint and original Host home. OrbStack's `docker-tools` multicall executable
is supported with a fixed `docker` argv[0].

```sh
reach project register /trusted/location/my-project.json
reach project inspect my-project
reach runtime health my-project
reach runtime inspect my-project
reach --project my-project exec -- 'id; pwd; hostname'
printf 'Guest content\n' | reach --project my-project fs write notes.txt
reach --project my-project fs read notes.txt
reach --project my-project claude
reach --project my-project codex exec --ephemeral 'Describe the project'
```

Registration checks the current runtime before saving a new immutable binding.
A second registration with the same ID fails; P0 has no rebind command. Names may
identify a container for initial registration, but all subsequent operations use
its captured full ID. Restarting the same container changes `StartedAt` and
invalidates that binding. Recreating a same-named container cannot resume it.

Keep `$REACH_HOME` (default `~/.reach`) on the Host, outside every Guest mount.
Project records live in `projects/<id>.json`; session schema v2 stores the project
ID and a digest of the complete registered record. Existing v1 unmanaged sessions
remain readable. Newer schemas, corrupt sessions and failed managed checks never
trigger automatic recreation; `--fresh` cannot downgrade a managed session.
Read-only lookups do not create missing directories. Record writes use a unique
temporary file, file sync, rename and (on Unix) parent-directory sync, with bounded
cross-process directory locks. Stale locks fail visibly and are not stolen.
Windows preserves file flushing and same-directory replacement via `os.Rename`
without attempting an unsupported read-only directory flush. Windows replacement
atomicity follows the Go/filesystem contract; directory-entry survival after power
loss is not promised there. Existing dangling session links are corrupt records,
never empty slots that may be overwritten.

Project IDs contain 1–64 ASCII letters, digits, `_` or `-`, beginning with a
letter or digit. Managed session names start with `project-`; long IDs use a
deterministic SHA-256 suffix to stay within the 64-character session limit.
`--project` forbids reach's `--session` override. Arguments after an explicit
`--` remain Guest arguments, including an application's own `--session` option.

## Initialize managed policy

Before managed exec, file mutation or a harness readiness probe, explicitly run
`reach policy init PROJECT_ID` on the Host. Guest effects then default to allow
and Host capabilities to deny. A missing policy returns `policy_unavailable`;
reads/inspect do not create one. See [POLICY.md](POLICY.md) for exact approvals,
mandatory journal, 64 KiB canonical request ceiling, clocks and recovery limits.

## Runtime and storage contract

Each new dispatch verifies:

- Native external-volume UUID, name, writable/non-internal status, actual mount
  table and exact mountpoint, plus a small regular random sentinel and SHA-256.
  Sentinels must remain outside Guest-visible binds. No missing directory is
  created by these checks. The sentinel, engine directory and each bind source
  must actually reside on that volume; an unapproved nested mount is refused.
- OrbStack's actual `data_dir` from the fixed native
  `control_home/.orbstack/vmconfig.json`. Unknown/missing formats fail. No `orb`
  command is run, since a nominal query may start the engine. The `~/OrbStack`
  file-browser view is not evidence of engine storage placement. The fixed
  `/usr/bin/dscacheutil -q user -a uid <current UID>` must return one matching
  UID and canonical account home equal to `control_home`; the harness's
  temporary `HOME` is not an account-identity source.
- The ambient context and its endpoint, then the fixed daemon endpoint and
  OrbStack daemon ID. Docker subprocesses use the fixed executable, `--config`
  and `--host`, original `control_home`, and a controlled environment. Conflicting
  `DOCKER_*` connection/configuration overrides fail before a Docker subprocess.
- Running/non-paused container status, full container and image IDs, `StartedAt`,
  project labels, explicit nonzero numeric UID and exact workdir. The immutable
  snapshot covers normalized mounts, labels and the complete HostConfig.
- The entire mount allowlist, including local named-volume driver, empty options
  and captured volume labels. Extra mounts, dangerous Host roots or credential
  paths, sockets, SSH agents, privileged/device access, added capabilities,
  shared Host namespaces and Host port publication fail. `cap_drop: ALL` and
  `no-new-privileges` are mandatory. Binds cannot overlap the engine data or
  trusted control files in either direction.

Host overlap checks compare real filesystem identities and ancestors, including
case aliases and regular-file hard links. A missing control path is inspected
through its nearest existing ancestor. Existing control subdirectories and the
session's records, settings and Codex home files cannot be symlinks; existing
control file leaves and sentinels must have a single hard link. These checks
do not claim to resist concurrent malicious changes by the Host account itself.

For `guest-owned`, mount an approved local named volume at `guest_path` and omit
`host_path`. [The Dockerfile and Compose template](../examples/orbstack/compose.yaml)
create UID/GID 10001, common development tools, an init process, resource limits
and a dedicated bridge without published ports. Provisioning is an explicit
operator action, after verifying external Storage. These templates are not run
by reach. Images and dependencies are build-time choices; registration captures
the resulting image ID.

For `shared-project`, set `host_path` and list the exact bind under `bind_mounts`,
with the same target as `guest_path`. Claim the source's external volume as well
as the engine volume. Replace the template's named-volume mount with long bind
syntax, including `bind: {create_host_path: false}`. Approve only a project root,
never HOME, `/Volumes`, Docker/SSH credentials or the control directory. All
source paths must exist and be canonical; a symlink spelling is refused.

## Harness and channel behavior

Only Claude and Codex are verified managed entrypoints. A managed launch requires
a recognized version and a **fresh successful** offline seam probe, with a hash
of the selected executable taken before any version execution and checked after
version/probe execution. Both probe exit 0 and the observed canary are required.
The lookup alias, resolved executable and their ancestors must be outside every
Guest-writable bind. Version, probe and launch use that same resolved executable,
a trusted cwd and a cleaned Host PATH/environment. It never trusts the legacy
version-only verdict cache. Unknown, bypassed, inconclusive and failed probes
refuse launch. A probe is evidence for the measured installed binary and tool
surface, not a guarantee against arbitrary future behavior or mutable runtime
dependencies.

Managed Claude uses controlled settings, native Bash and one fixed strict MCP
bridge with only `host_plan`, `host_execute`, `host_status`. Its exact generated
bridge callback enters a typed handler through the shell prefix; it does not
execute a Host shell. Managed Codex has its own controlled `CODEX_HOME`, fixed
Host MCP configuration and exec-server; only auth is copied from the operator's
Codex home. Operator approve/revoke are never model tools.
Both launch from trusted control directories, outside Guest-writable project
configuration. The probe uses the same strict tool/configuration options and
controlled-directory posture, with temporary homes and dummy mock-model auth.
The probe deliberately differs in provider configuration and headless prompt;
it does not exercise every interactive UI path. Strict Codex probes use only
the configured exec-server seam, without a legacy shell shim fallback. Claude
probes check the current persisted Guest cwd, matching the shell-prefix launcher;
Codex checks its mapped project workspace.

Codex protocol paths accept both the original Host workspace spelling and its
resolved symlink spelling, including macOS `/var` versus `/private/var`. Both map
to the Guest workspace. Reach never rewrites a user's shell command to route it.
A failed Guest `cd` exits before any environment export or command body, including
later semicolon/newline-separated commands. The strict probe also checks the
Guest cwd, so an absolute canary cannot conceal a broken cwd mapping.

Managed pass-through arguments use a small allowlist: model selection and basic
print/exec/output controls. Claude accepts at most one print prompt and rejects
`cc://`/`cc+unix://` command routing; Codex accepts interactive mode or `exec` with
at most one prompt, excluding `resume`/`review` subcommands. `--force`, local-file-tool
opt-in, mirror mode, custom settings/environments/MCP/plugins, cwd/config overrides and unverified harnesses
are refused. Known shell/Node injection and reach routing overrides are scrubbed
from both launcher and probe environments. The Docker subprocess does not
inherit the Host environment. An exec-server request's explicit `env` map is
still passed to the Guest; whether a particular harness includes Host variables
there must be checked with a fictitious environment canary in native conformance.
Do not infer credential isolation solely from Docker's controlled environment.

Claude's internal local callback can invoke only the exact currently generated
reach hook through its typed handler. `CLAUDE_PROJECT_DIR` does not authorize a
raw Host command. Damaged/missing managed sessions and malformed hook input deny
the tool instead of returning an empty allow response. Legacy unmanaged callback
behavior remains available for legacy sessions. Hook registration and verification
use the same resolved reach executable even when invoked through its shell-prefix
symlink; the complete callback command must still match exactly.

Managed child entrypoints carry the expected project and record digest. Every
new dispatch also rereads the persisted session association; removal, downgrade
or replacement invalidates old children and live servers. The initial capability
probe uses the explicitly approved registered binding before its session exists.

Claude cwd bookkeeping accepts only the measured `/tmp/claude-<hex>-cwd` form
(or the same system temp root's canonical spelling). It validates the destination
before Guest dispatch, anchors directory operations, and atomically replaces a
fresh private file instead of following a symlink or truncating a hard-link target.
Other Host destinations are refused.

On runtime failure, subsequent file calls, process starts, stdin writes and
environment readiness/info fail; old file pipes and managed transport streams
are closed best effort. Any entrypoint that first discovers a failed guard
permanently retires that server's file pool and stdin streams; restoring the guard
requires a new server connection. Close/terminate remain available for cleanup.
An already dispatched operation can continue in the Guest; a check is not instantaneous I/O
revocation. P0 does not implement Guest PTYs or verified signal delivery:
managed `tty` and `process/signal` explicitly report unsupported. Closing/killing
the local Docker client does **not** prove the Guest process has terminated.

## Managed paths and command cwd

Managed file operations use the registered `guest_path` as their root, including
relative `reach fs` paths; changing a shell session's cwd does not change this
file root. Guest absolute paths must remain beneath it. Shared projects also
accept their explicitly registered Host project path. Codex protocol paths may
use the controlled harness cwd's original and canonical spellings (for example
`/var` and `/private/var`). Prefix siblings, other Host paths and Guest-owned
Host mirror paths gain no authorization. Paths use POSIX Guest semantics.

Host protocol aliases and `guest_path` must not strictly contain one another by
path components. This applies to the registered shared `host_path` and every
trusted harness cwd spelling, including its canonical spelling. Exact equality
is allowed; prefix siblings such as `/project` and `/project-other` do not
overlap. For example:

| Host alias | Guest root | Result |
| --- | --- | --- |
| `/Volumes/DISK/project` | `/workspace/project` | allowed |
| `/Volumes/DISK/project` | `/Volumes/DISK/project` | allowed |
| `/Volumes/DISK/project` | `/Volumes/DISK/project/guest` | `path_denied` |
| `/Volumes/DISK/project/host` | `/Volumes/DISK/project` | `path_denied` |

Strict nesting would give one `file://` string two meanings, including when a
canonical Guest URI is reused in a later request. Reach rejects the mapping
before dispatch instead of changing its prefix priority or guessing which file
was intended. Existing records remain readable by inspect/health and are not
rewritten; managed dispatch refuses an ambiguous mapping. Prepare a separate
Guest root such as `/workspace/project` and explicitly register a new project
when its container binding changes. There is no automatic migration or rebind.

The trusted entrypoint fixes the namespace: raw CLI input uses `Protocol`;
exec-server maps its URI once, then uses `Guest` for underlying FileOps, pool
members, derived paths and handles. Reopening a channel preserves that choice.
`file:` URIs are decoded once and reject authorities (including `localhost`),
queries, fragments, bad encoding, NUL and overlong values. A doubly encoded
percent sequence remains a literal filename rather than being decoded again.

The fixed-root helper holds an `os.Root` directory descriptor. Every read,
write, rename, copy, remove, directory traversal, search, glob and canonicalize
uses that root; both copy/rename paths are approved before dispatch. There is no
shell `cp`, `readlink`, `find` or `grep` fallback for managed FileOps. A symlink
that resolves outside the root is refused; removing the symlink itself remains
allowed and recursive remove never follows it. The workspace root cannot be
removed, renamed or replaced. Absolute symlink targets are refused by `os.Root`,
even if their spelling points back inside the root. Internal relative symlinks
can resolve safely. Copying symlinks is explicitly unsupported. Content reads,
writes, hashes, copies and searches reject multiply linked regular files;
ordinary Guest shell operations retain their own semantics.

Managed writes use an exclusive random same-directory temporary file and atomic
rename. Pools and reopened channels require the same root identity and project
digest; file handles belong to one exec-server connection and every use rechecks
the shared runtime guard. A failed runtime guard permanently retires that
connection, including its old file handles and stdin channels; cleanup stays
available. Request metadata is capped at 64 KiB before allocation; response and
content frames plus aggregate reads/writes are capped at 64 MiB. Hash and search actual reads are
bounded at 64 MiB; search limits one line to 1 MiB. Walk/search/glob visit at most
10,000 entries at depth 128 and return bounded frames. Client cancellation closes
the channel; an already dispatched Guest operation can continue within its budget.
Glob patterns use Go `path.Match` syntax, matched against relative paths when
they contain `/` or each filename otherwise; directory symlinks below the selected
traversal root are not followed.
Search uses Go regular expressions, with literal and case-insensitive options.
These managed search/glob semantics do not inherit ripgrep ignore-file rules.

Managed CLI exec, process/start and Claude Bash use the same one-shot Guest
launcher: it opens the root and cwd directory descriptors, checks the negotiated
root identity, changes directory through the held fd and execs the command. A
bad cwd fails before environment exports and the command body. No later
Transport layer adds a string `cd`, and the user's command text is unchanged.
Physical cwd reports are base64 framed; managed cwd containing CR or LF is
explicitly refused, while ordinary FileOps filenames retain POSIX newline
semantics. The acquired directory instance stays fixed across symlink swaps; this does not
prevent another Guest process from moving that directory after acquisition.
A command may itself `cd` outside the project or access Guest `/usr`, caches,
temporary directories and configured network services. A resulting outside cwd
cannot be persisted or used by the next managed call, and is not written to
Claude's narrow Host cwd callback. This is a starting-directory contract, not an
OS shell sandbox or isolation from hostile same-UID Guest processes.

## Validation and limits

`make check` runs vet plus race tests; `make lint` and normal/cross builds are
additional checks. Unit tests cover changed context/daemon/container generation,
storage, dangerous mounts, fixed Docker argv/environment, zero dispatch on
rejection, existing file pools/stdin channels, schema downgrade and strict harness
failure paths. Native macOS volume/runtime checks and real Claude/Codex mock-model
conformance are separate acceptance steps. Use:

```sh
reach harness verify claude --session project-my-project \
  --task-prefix 'printf canary > /workspace/project/canary; cat /workspace/project/canary'
reach harness verify codex --session project-my-project \
  --task-prefix 'test -z "${REACH_HOST_ONLY_CANARY+x}"'
```

For a managed session these commands use the same strict probe path as launch,
including controlled Claude settings, executable fingerprint and no verdict
cache. Use only fictitious canary values; never print real credentials.

The separate [`test/conformance/managed`](../test/conformance/managed/main.go)
helper exercises the actual `reach --project ... claude|codex` process replacement
after its strict readiness probe. Build it with Go in the development Guest
(cross-build with `GOOS=darwin GOARCH=arm64 CGO_ENABLED=0` for macOS), then run:

```sh
REACH_HOME=/trusted/isolated-test-state managed-conformance \
  --reach /absolute/test/reach --project my-project \
  --harness codex --binary /absolute/test/codex --mode patch
```

Use `--mode shell` for Codex or Claude. For a Claude session whose persisted
Guest cwd is intentionally non-root, supply `--guest-cwd /the/expected/path`.
It creates a fresh temporary `HOME`, an empty source `CODEX_HOME`, fictitious keys and a localhost mock, with an explicit
small environment. Codex needs a test-only PATH executable that prepends the
mock provider flags; it does not relax the production argument allowlist. The
helper prints the real harness binary hash separately from the readiness probe's
wrapper fingerprint. Codex's `gpt-5.4` name selects metadata bundled with 0.148.0
to advertise native custom `apply_patch`; no request reaches that model service.
The patch test uses the native custom tool, checks its result, independently reads
the relative file in the Guest and checks that the corresponding Host control cwd
file is absent. Shell tests check Guest cwd, read/write and a fictitious Host env
canary. Random Guest canary files are retained as test evidence; temporary Host
homes/wrappers are removed. Shared bind contents appearing at their approved Host
source are expected. This is offline headless conformance, not paid-model or
interactive-terminal coverage; PTY/signal limitations above still apply.

`--task-prefix 'apply_patch ...; test ...'` is a shell command, not proof of native
Codex patch support. Codex 0.148.0 only intercepts recognized patch-only shell
forms, while its native tool operates through the selected executor filesystem.
An arbitrary compound shell script needs its own explicitly provisioned tools;
readiness never installs an `apply_patch` executable in the Guest.

Do not run the legacy integration suite's fixed-name Docker cleanup against a
user daemon. Native acceptance should use dedicated random names/labels and exact
IDs, and preserve its evidence separately. No physical unmount, engine restart,
LAN reconfiguration or storage migration is implied by P0 validation.

Mounts define Host file visibility. P1 restricts managed FileOps and command
**starting cwd** to the project root; it does not restrict arbitrary Guest shell
commands after they start. A dedicated
bridge also does not prevent Guest access to reachable Host/network services.
OrbStack's Guest IP/`.orb.local` routes and LAN reachability require separate
network validation. An image ID does not prove an unchanged writable rootfs.
The Host user can bypass reach and run Docker directly. P2 adds mandatory
security intent/result, explicit policy initialization and typed Host planning,
approval and revocation; see [POLICY.md](POLICY.md). Production `open_url` still
returns `exposure_unavailable` without P4, and port/service/runtime drivers are
unsupported. Persistent terminals and reconciliation remain later work.

To roll back code, revert the phase commit and retain records/resources. An older
binary must refuse new session schemas; do not force it to rewrite managed state.
Code rollback does not delete containers, volumes or project data. Keep any stale
lock or corrupt record as evidence until the operator performs an explicit repair.
