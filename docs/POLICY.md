# Managed policy, Host capabilities and security ledger (P2)

Managed side effects require an explicitly initialized policy. Registration,
inspect, health, fixed helper negotiation and reads do not initialize it:

```sh
reach policy init my-project
reach policy inspect my-project
```

Initialization permits ordinary Guest effects and denies every Host capability.
Unmanaged Local/SSH/container behavior and the optional legacy operation log
retain their existing contracts. `REACH_NO_AUDIT` cannot disable the managed
security ledger. Guest Shell still has the container user's ordinary filesystem
and network permissions; this policy is not an OS shell sandbox.

## Operator and model interfaces

Only the Host operator CLI can change policy or approve/revoke. Managed children
are denied this entrypoint. The fixed MCP bridge advertises exactly
`host_plan`, `host_execute`, `host_status` for Host capabilities, plus the nine fixed typed P3 Guest terminal tools documented in [TERMINALS.md](TERMINALS.md); it has no operator or arbitrary Host argv method. Its project/session/record binding comes from trusted configuration.
Claude admits only the exact generated bridge callback through its shell
prefix. Codex keeps `include_local=false`: its fixed MCP server selects the
`reach` executor, where only the exact binary/argv/cwd/binding/env shape enters
an in-process typed MCP handler. No Host shell/program is spawned from that
request. All ordinary process argv still use the Guest. MCP protocol frames
are control traffic; effects requested through them use the same ledger.
Codex marks this server required and grants transport-level approval only to
these fixed tools; operator approval remains exclusively in reach policy.
Each MCP connection permanently retains its first runtime/identity guard
failure, including for status calls. Restoring the resource requires a new
connection. Ordinary policy, approval, clock and ledger rejections do not
retire an otherwise valid connection.

```sh
reach policy set my-project open_url approval
printf '%s\n' '{"capability":"open_url","target_id":"EXPOSURE_ID","generation":1,"params":{"path":"/"}}' |
  reach host plan my-project
```

Review the complete returned `request`, its binding, generation, resource scope,
effect and `request_digest`. Approval requires that exact digest:

```sh
reach policy approve my-project REQUEST_ID EXACT_REQUEST_DIGEST
reach policy approve my-project REQUEST_ID EXACT_REQUEST_DIGEST 2m
printf '%s\n' '{"request_id":"REQUEST_ID","approval_id":"APPROVAL_ID"}' |
  reach host execute my-project
reach policy revoke my-project APPROVAL_ID
```

Actual request and approval IDs are random 32-character hex values, not the
placeholders above. A grant is single-use, defaults to five minutes and has a
maximum TTL of fifteen minutes. Revocation prevents future consumption; it
cannot undo an action already dispatched. Every policy change advances its own
revision and invalidates earlier plans and approvals. Project schema 1,
revision/generation 1 and session schema v2 remain unchanged.

Rules are `allow`, `approval` or `deny` for `open_url`, `port_expose`,
`service_change`, `runtime_change`. Guest has only `allow`/`deny`. Host defaults
to deny; `allow` is an explicit operator grant, never inferred from shell text.
Unknown capabilities, arbitrary Host shell, key access and Xcode are refused.

`open_url` accepts only an exposure ID, generation and origin-relative path. It
rejects URL origins, query/fragment, control characters, backslashes, traversal
and encoded authority changes. The executor constructs a loopback URL from a
healthy, same-binding registry observation, checks it again before dispatch and
uses fixed `/usr/bin/open` argv on macOS. Browser navigation after opening is
outside this guarantee. P2 has no production P4 exposure registry, so production
execute returns `exposure_unavailable`. Port/service/runtime drivers return
`unsupported`. These rejections happen before consumption/intent. Successful
browser dispatch is verified only with a trusted registry/executor injected in
tests; there is no model-accessible bypass or test-mode environment variable.

`host_status {}` returns policy and retention counts. Supply exactly one
`request_id` or `operation_id` for its detailed status. It never returns the
private HMAC key or raw Guest commands, stdin, environment or file contents.
`reach policy inspect` is the operator's complete retained event view.

## Durability, privacy and failure semantics

Each project uses `REACH_HOME/security/PROJECT.json`: one strict schema-1
envelope containing policy, private HMAC key, pending plans, approvals,
operations, ordered events and a checksum. A bounded directory lock serializes
writers across processes. Approval consumption and intent are a single atomic
replacement, flushed as file → rename → containing directory before dispatch.
The ledger is mandatory for CLI/Claude exec, Codex process start and stdin,
managed write/mkdir/remove/rename/copy, including native patch file mutations, and P3 terminal create/write/resize/interrupt/close.
Each primitive mutation has its own intent/result; a multi-file patch is not an
atomic filesystem transaction. Fixed internal handshakes and reads do not
recursively create execution intents. Close and best-effort termination cleanup
remain available after guard or policy failure.

No durable intent means zero user-effect dispatch. A dispatch transport error
or a result-persistence failure can mean the effect happened; result write
failure returns `outcome_unknown` with the operation ID. A dispatched error
returns the same classification and identity even when its result record was
saved successfully, preserving the original cause for the caller. MCP error
content also retains the returned operation for reconciliation. An unresolved `intent`
is also an unknown outcome. Inspect it and reconcile the actual resource; never
automatically repeat it. Re-executing the same Host request is refused even
after a crash or a successful result. This is not distributed exactly-once
execution. Managed process stdin IDs retain successful and unknown outcomes for
the connection and refuse changed payloads/replay; they do not become P3
persistent terminal IDs.

The store rejects duplicate/unknown JSON fields, extra documents, invalid
schemas, checksum/tail corruption, symlink/hardlink control leaves, unsafe
ownership/permissions and mismatched opened-file identity. It does not repair,
truncate or rotate corrupt/full state. A stale lock is not stolen. The checksum
helps detect corruption; a same-UID Host attacker can rewrite the entire store
or bypass reach, which is outside this trust boundary.

Guest payloads use a private per-project key and HMAC over the complete binding,
policy revision, capability and full parameters. Raw payloads and external
error/output strings are not stored. Only necessary typed Host plan arguments
are retained for review. Events include schema/sequence/random ID, native time,
binding, revision, actor, capability, request/operation/approval identity,
digest, target/generation, phase and reason/outcome. Terminal/service output is
not part of this security ledger.

## Clocks and budgets

Darwin uses public `CLOCK_MONOTONIC_RAW` and `kern.bootsessionuuid`; Linux uses
`CLOCK_BOOTTIME` and the kernel boot ID. Deadlines use monotonic nanoseconds,
independent of wall-clock expiry. Same-boot processes can consume approvals.
A boot change, unavailable clock, observed monotonic rollback, or wall-clock
value below the durable/issued high-water mark refuses mutation. Checks repeat
after potentially slow runtime/registry validation and before saving. This does
not detect every small wall-clock adjustment. It does not provide an automatic
post-reboot policy reset; retain the ledger and perform explicit reconciliation
with an appropriate future recovery workflow. Native Windows policy execution
is unsupported; a Windows cross-build is not a native Windows acceptance test.

| Budget | Hard limit |
| --- | --- |
| Canonical request / MCP frame | 64 KiB |
| Ledger | 16 MiB |
| Pending requests / approvals | 256 each |
| Operations / events | 4096 / 8192 |
| Fixed MCP control streams per exec-server | 1 |
| Managed stdin write IDs per process | 256, no silent eviction |
| Lock wait | At most 3 seconds, bounded by the caller context |

The canonical Guest request includes JSON/base64 encoding overhead and complete
parameters, so a single file/stdin payload may reach the 64 KiB ceiling before
64 KiB of raw data. Oversized effects are rejected, not truncated. Result-event
slots are reserved for outstanding intents; reaching any retention limit stops
new effects. P2 does not silently rotate security history or provide a reset
command. Keep records as evidence and plan explicit retention/recovery before
continuing a full ledger.

Rollback: revert the P2 phase commit and stop using the new bridge. Keep project,
policy, approvals, ledger and user data. Never delete audit state or downgrade
project/session schemas to manufacture compatibility. Reverting to P1 also
removes P2's mandatory authorization/audit guarantee.
