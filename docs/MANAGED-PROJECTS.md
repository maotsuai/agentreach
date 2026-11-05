# Managed OrbStack projects (P0)

Managed projects add an explicit Host authorization around an **existing**
OrbStack Docker container. Ordinary SSH, local, Docker and Podman sessions keep
their existing behavior. P0 never pulls, builds, creates, starts, restarts or
automatically rebinds a runtime. `runtime health` means inspect and verify.

The control plane runs on macOS. Linux tests inject volume inspectors; a Linux
binary refuses native OrbStack volume checks. Guest commands and file operations
execute through Docker, including requests over already-open file helper pools
and exec-server stdin channels.

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

Managed Claude uses controlled settings only, an empty strict MCP configuration
and the Bash-only tool set. Managed Codex has its own controlled `CODEX_HOME` and
exec-server configuration; only auth is copied from the operator's Codex home.
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

Mounts define Host file visibility. P0 does not restrict arbitrary Guest shell
commands to the project directory; workspace PathMapper policy is P1. A dedicated
bridge also does not prevent Guest access to reachable Host/network services.
OrbStack's Guest IP/`.orb.local` routes and LAN reachability require separate
network validation. An image ID does not prove an unchanged writable rootfs.
The Host user can bypass reach and run Docker directly. Mandatory security audit,
typed Host capabilities, persistent terminals, services/ports and reconciliation
belong to later phases and are not delivered by P0.

To roll back code, revert the phase commit and retain records/resources. An older
binary must refuse new session schemas; do not force it to rewrite managed state.
Code rollback does not delete containers, volumes or project data. Keep any stale
lock or corrupt record as evidence until the operator performs an explicit repair.
