# Persistent managed text terminals (P3)

P3 exposes a tmux text terminal through the Host CLI and the fixed Claude/Codex
MCP bridge. The Guest container must remain running. A terminal survives complete
Host CLI, bridge and harness exits; it does not restore REPL memory after a
container restart. Ordinary exec-server processes retain their existing
nonpersistent semantics, including their unsupported managed `tty`/signal API.

## Explicit preparation

Prepare the current Linux `reach-helper` at `/usr/local/bin/reach-helper` as
specified in [MANAGED-PROJECTS.md](MANAGED-PROJECTS.md). The terminal helper
currently requires the experimentally tested `/usr/bin/tmux` **3.3a**. It neither
installs nor upgrades tmux/helper during reads, health checks or dispatch. Prepare
the approved helper on a replacement, then use an explicit
[rebind or new registration](RUNTIME-RECOVERY.md). Initialize policy with
`reach policy init PROJECT`. Create a new terminal for the new binding.

The backend uses a per-terminal private socket, `/dev/null` configuration,
controlled Guest environment and random names. It never connects to the default
tmux server. Its files live in Guest `/tmp/agentreach-terminals-UID/TERMINAL_ID/`,
on the container's externally stored engine filesystem. They are outside the
public rooted FileOps API. Guest users of the same UID can tamper with the
backend and output: these logs are not authenticated security evidence.

## CLI and MCP

Every CLI operation reads one strict JSON object from stdin:

```sh
printf '%s\n' '{"request_id":"0123456789abcdef0123456789abcdef","argv":["/usr/bin/python3","-q"],"cols":80,"rows":24}' |
  reach --project PROJECT terminal create
printf '%s\n' '{"terminal_id":"TERMINAL_ID","input_id":"11111111111111111111111111111111","data":"eD00Mg=="}' |
  reach --project PROJECT terminal write
printf '%s\n' '{"terminal_id":"TERMINAL_ID","input_id":"22222222222222222222222222222222","key":"Enter"}' |
  reach --project PROJECT terminal write
printf '%s\n' '{"terminal_id":"TERMINAL_ID","max_bytes":16384}' |
  reach --project PROJECT terminal read
```

Replace the terminal placeholder with the returned random 32-character lowercase
hex ID. Request/input IDs use the same syntax and are caller-selected unique
identities. The example data decodes to `x=42`. Generate fresh IDs for new effects;
retain an ID and its exact parameters when querying/retrying the same effect.
Do not replay an unknown result under a new ID.

The fixed bridge adds exactly nine typed Guest tools to the existing three Host
tools. Claude's controlled allowlist and Codex's `enabled_tools` contain this
same catalogue. There is no operator or arbitrary Host argv tool, and Codex still
uses `include_local=false`. Untrusted project MCP/configuration cannot add Host
tools. The exact generated callback enters an in-process typed adapter.

| CLI operation / MCP tool | Parameters |
| --- | --- |
| `create` / `terminal_create` | `request_id`, `argv[]`, `cols`, `rows`; optional `cwd` |
| `write` / `terminal_write` | `terminal_id`, `input_id`, exactly one of base64 `data` or `key:"Enter"` |
| `read` / `terminal_read` | `terminal_id`, `max_bytes`; optional `cursor` (omitted means offset zero) |
| `resize` / `terminal_resize` | `terminal_id`, `cols`, `rows` |
| `interrupt` / `terminal_interrupt` | `terminal_id`; optional `signal:"Ctrl-C"` |
| `inspect` / `terminal_inspect` | `terminal_id` |
| `list` / `terminal_list` | `{}`; retained Host identities/state, not a live process scan |
| `attach` / `terminal_attach` | `terminal_id`; new identity/guard verification, no input replay |
| `close` / `terminal_close` | `terminal_id` |

Unknown fields, duplicate JSON keys and fields belonging to another operation
are refused. There is no generic signal, shell-control, backend path, socket or
project-override argument. `argv` is a UTF-8 array without NUL; empty arguments,
quotes, whitespace and newlines remain individual arguments. The rooted launcher
holds the approved directory before the startup barrier, then the pane helper
execs the array. Ordinary Guest shell permissions still apply after startup.
The initial tmux environment fixes PATH to `/usr/bin:/bin`, HOME to
`/nonexistent`, LANG to `C.UTF-8` and TERM to `xterm-256color`; use absolute
executables for tools outside that PATH. No Host environment or credential is
forwarded. Applications may choose their own environment through a Guest program.

## Input and durable outcomes

Terminal identity binds the immutable project digest, daemon/container ID and
StartedAt digest, backend nonce, tmux server boot/PID/start-time, session/pane and
log epoch. Attach checks the original identity; a same-named socket, session,
pane, PID or replacement container cannot inherit it. Closed/lost IDs are never
reused. A connection permanently latches its first runtime/identity failure, including backend nonce/instance mismatches and helper identity rejections;
recovery requires a new connection and an unchanged real backend. The MCP latch also covers the existing Host status tools.

A private Host schema-1 terminal envelope lives at
`REACH_HOME/terminals/PROJECT.json` for project schema v1. Project schema v2
uses the equivalent path under `REACH_HOME/epochs/FULL_RECORD_DIGEST`. It stores
object identity, keyed parameter
digests, input IDs, operation IDs and necessary state; raw argv/input are not
persisted there. The P2 ledger remains authoritative for policy, durable intent
and result. Lock order is terminal-project lock → policy ledger lock, never the
reverse. These are separate records, not a distributed transaction.

Before every new create/write/resize/interrupt/close dispatch, the P2 Guest
policy must permit the effect and persist its mandatory intent. Creation saves
its random nonce/terminal identity before dispatch. Writes durably reserve an
`accepted` input entry, save `sending` before dispatch, then save `sent` or
`outcome_unknown`. Confirmed duplicates return their original accepted/input/operation result without dispatch, including after close or durable loss; changed parameters are refused. Accepted/sending/unknown
entries are retained and never automatically replayed, including after a full
process restart. The response `accepted:true` means tmux acknowledged the input,
not that the application executed it successfully.

A transport failure or result-record failure returns `outcome_unknown` and the
operation ID, even if the ledger result itself was saved. A failed terminal
result write can leave `sending` while the ledger says `ok`; the retained input
still refuses replay. An intent/write failure before dispatch sends no input.
Use inspect/ledger evidence and the application's state to reconcile manually.
No automatic repair, lock stealing, rotation or input eviction is provided.

Creation with a repeated request ID returns the retained terminal, or its same
unknown result. It never creates a second backend. If dispatch happened but the
Host result record failed, inspect can discover the exact nonce-backed orphan;
explicit attach adopts its complete observed backend identity. It does not
recreate a missing process. A stale lock remains evidence and fails within the
lock budget. List can expose a retained creating identity without acquiring that
resource lock; explicit repair belongs to a separate reviewed recovery action.

## Original PTY bytes, cursor and completion

Output comes only from tmux `pipe-pane`, not screen snapshots. A fixed Guest
collector owns a single bounded byte log; it continues when Host/harness clients
exit. stdout/stderr are merged by the PTY. Terminal line discipline can transform
program output (for example LF → CRLF) and input; the returned bytes are the PTY
stream, not separately authenticated program stdout/stderr.

Each stream has a random, nonreusable epoch. Cursors encode terminal ID, epoch
and a canonical nonnegative byte offset. Read returns base64 `data`,
`next_cursor`, `earliest_available_cursor`, `state`, `complete` and an optional
`gap`. Reusing a retained cursor rereads the same retained bytes; newly appended live-tail bytes may extend a later response. Empty `data` may be omitted. Invalid/future cursors
and foreign/old epochs return `output_gap`. UTF-8 can split at any byte boundary;
clients must carry partial code points between reads instead of altering bytes.
There is no rotation, so the earliest retained offset is zero.

Startup waits until both the rooted pane and collector are ready before
releasing the user program. Natural pane destruction waits for tmux's pending
pipe bytes to drain. An internal control pane keeps the natural `pane-exited`
hook context alive; the hook writes exit evidence and closes that control pane.
Only after collector EOF, log fsync and durable final offset, together with that
natural-exit evidence, can `complete:true` be returned. Each active terminal
therefore uses its user pane plus one internal sleeping control pane. Neither is
a Host daemon or an additional general PTY supervisor.

`state:"exited", complete:false` can mean collection is still draining.
`complete:true` describes collection, not how much this read returned: continue
reading from `next_cursor` until the returned data is empty. The natural-exit
path does not guarantee an application exit code; an absent `exit_code` is
unknown, not zero. The pane leader's exit ends the terminal; detached descendants
and their later output are not supervised.

Explicit early close returns `closed_before_drain`, `complete:false`, and
`descendants:"unconfirmed"`. It targets only this verified user/control pair;
it does not kill the default server, other terminals or the container. Killing
a pane does not prove that detached descendants died. Close after a fully
collected natural exit can retain its complete result. Policy/identity failures
cannot be bypassed through a cleanup path.

## Hard budgets and failures

| Item | Hard ceiling |
| --- | --- |
| Public/internal/MCP frame | 64 KiB, including JSON/base64 and binding overhead |
| Input data / read data per call | 16 KiB each |
| Argv count | 128 |
| PTY dimensions | columns 1–500; rows 1–300 |
| Log per terminal | 8 MiB |
| Retained terminals per project | 64, including closed/lost records |
| Retained input IDs per terminal | 256; no eviction |
| Host terminal envelope | 8 MiB |
| Cross-process lock wait | at most 3 seconds |
| Guest control operation / pane startup barrier | 10 seconds / 15 seconds |

The existing P2 ledger limits also apply and may be reached first. Oversized
requests fail instead of truncating input. At output exhaustion the collector
retains the first 8 MiB, records `output_limit`, stops collection and closes only
that terminal's panes; `complete` stays false. Available prefixes can still be
read alongside their explicit gap.

Gap reasons include `collector_state_missing`, `collector_failed`, `log_missing`,
`log_changed`, `log_truncated`, `output_limit`, `exit_unconfirmed`,
`backend_missing`, `backend_lost`, `backend_unavailable`, `closed_before_drain`,
`close_state_invalid`, `runtime_unavailable`, `runtime_lost`, `backend_identity_changed` and `resource_lost`. Read may return a typed gap with a
successful transport response: callers must inspect it. Missing/truncated or changed logs never reset the old offset and pretend to be the old stream. Every entrypoint persists a confirmed lost state and its typed gap before returning or refusing an effect; restoring a log does not revive that ID. Retained terminal records carry the reason across process restarts.

Temporary runtime uncertainty is `unavailable`. A confirmed binding/generation
change is `lost`, preserves operation/terminal identity and refuses dispatch.
Container restart loses REPL memory. Create a new terminal after explicit
rebind or new registration.

## Rollback

Revert the P3 phase commit and stop using the new terminal entrypoints. Preserve
project/session/policy records, security ledger, terminal identities and Guest
logs as evidence. Do not downgrade schemas or replay inputs. Any cleanup must
name the exact owned terminal/fixture; do not prune shared resources or kill the
user's default tmux server. P5 rebind retires the original terminal references,
including on the same container ID with a new StartedAt.
