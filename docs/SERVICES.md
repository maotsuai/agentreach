# Managed services and loopback ports (P4)

P4 adds typed sidecar and non-interactive workspace services to an existing
managed project. Each service retains its original binding and P2 operation
identity. Sidecars use a generated Compose file. See the
[P4 validation and environment limits](validation/P4.md).

After [P5 migration or rebind](RUNTIME-RECOVERY.md), create new services and
exposures in the new binding. Dynamic listeners and streams are retired by
their binding checks. A fixed Docker publication stays with its sidecar until
an explicit lifecycle action stops that exact resource. P5 can plan and stop
a proven retired sidecar or exit its original drained manager.

## Service specifications and authority

`ServiceSpec` has a closed schema: `schema:1`, `project_id`, `driver`, immutable
local `image` ID for sidecars, `argv`, absolute `cwd`, non-sensitive `env`, exact
`mounts`, full `network_id`, non-root numeric `user`, `resources`, `health`,
`restart:"no"`, `log_bytes`, `guest_ports`, and fixed `publish` entries.

The Host validates image metadata, expands effective image environment, clears
ENTRYPOINT and image health commands, and requires every declared image VOLUME
to have an approved mount. Binds must match approved project mounts; local named
volumes require captured identity and project ownership, without driver options.
Host controls, credentials and Docker sockets are not accepted as mounts.
No Host environment, env_file, arbitrary Compose, build, pull, include, override,
profiles, privileged mode, devices, host namespaces or implicit dependencies are
available through this API.

`spec_digest` hashes the normalized specification. `execution_digest` also binds
the project binding, generated render and Compose executable identity. The
`spec_digest` field of a `service_change` **Host plan must contain the record's
`execution_digest`**, not its public specification digest. A plan is reviewable
metadata, not authority to dispatch; operator approval and durable intent remain
in the project policy ledger.

Sidecars use a private generated Compose file with explicit file/project/cwd and
an empty env-file source. The Docker configuration and CLI/plugin chain are
checked before invocation. Creation does not start the container. The Host
inspects the full created container configuration, exact mounts, network,
labels, resource limits and all PortBindings before starting that exact ID.
The configuration uses a non-root user, `cap_drop:ALL`, no-new-privileges,
bounded JSON logs, fixed resources and no automatic restart.

Workspace services reuse the P3 supervisor in a separate private namespace.
They inherit the fixed workspace container, user and resource limits; they cannot
change image, mounts or published Docker ports. The service API exposes no
interactive input and public terminal tools cannot discover/control its backend.
`stopped` means the supervised backend was closed. `descendants:"unconfirmed"`
explicitly avoids claiming that detached descendants have terminated.

## CLI and MCP workflow

JSON is read from stdin. All entrypoints select an already registered project.
The eight P4 MCP tools are `service_plan`, `service_start`, `service_stop`,
`service_inspect`, `service_logs`, `service_list`, `port_list`, `port_inspect`.
They join three Host tools and nine terminal tools; approval/revocation are
operator CLI functions and are not model tools.

1. Sidecar: `reach --project PROJECT service plan` with `{request_id,spec}`.
   Inspect the returned `service` and its exact `pending` request/digest.
2. Operator: `reach policy approve PROJECT REQUEST_ID EXACT_DIGEST`.
3. Agent: `host_execute {request_id,approval_id}` or
   `reach host execute PROJECT`. Retain the operation ID, including on errors.
4. Query `service_inspect {service_id}` until `ready:true`; `running` alone does
   not establish readiness. Logs use `{service_id,max_bytes}`; workspace logs
   also accept an epoch cursor and return base64 data.
5. Stop/restart a sidecar via `host_plan` with capability `service_change`,
   target service ID/current generation, and params
   `{operation:"stop"|"restart",spec_digest:EXECUTION_DIGEST}`, then exact approval
   and execute. Workspace start uses `service_start {request_id,spec}` under
   Guest policy; workspace stop uses `service_stop {service_id}`.

States include planned, created, starting, running, unhealthy, stopped, lost,
unavailable and outcome_unknown. Identity/generation failures retire the current
MCP connection; ordinary approval/readiness failures do not. Lost objects are not
resurrected when a same-name container or process appears. A fresh connection
rechecks the binding but does not revive lost identities.

Workspace log responses carry `next_cursor` and `earliest_available_cursor`
from the supervised stream. `gap` reports known missing or incomplete output
even when the read succeeds. `complete:true` confirms collection finished
without a gap. `exit_code` and `descendants` report separate observations when
available. A read error can include the service identity, the earliest cursor
and gap details. Advance with `next_cursor` after a successful read, and use
`earliest_available_cursor` to restart a read after an invalid or stale cursor.

Repeated request IDs must have the same payload. An unresolved intent never
dispatches again; query `host_status` by operation/request ID and inspect the
retained service. Result-storage failures may leave a completed effect with an
unknown outcome. Do not infer that a missing response means no effect occurred.

## Publications, dynamic proxy and browser

Fixed sidecar publications require explicit nonzero Host ports and
`listen:"127.0.0.1"`. Their exposure IDs bind the service generation, full
container ID, StartedAt and endpoint. Primary HTTP/TCP readiness is bounded and
does not follow redirects. Other approved ports require an additional TCP probe;
this does not assert their application protocol is healthy.

For a dynamic proxy, inspect a ready service, then use `host_plan` capability
`port_expose`, target service ID/current generation, and params
`{service_id,guest_port,host_port:0,listen:"127.0.0.1"}`. Approve and execute the
exact plan. The manager calls `net.Listen` once and retains that actual listener;
it does not probe a port, close it and rebind. A fixed conflict preserves the
occupant. Query `port_list`/`port_inspect` for the assigned port and operation.

Close a dynamic exposure with capability `port_expose`, target exposure ID,
target generation, and params
`{operation:"close",service_id,listen:"127.0.0.1"}`. Approval revocation and close
terminate established streams as well as listeners; TCP clients may observe EOF
or reset. Authorization is polled every second separately from target checks.
This is a polling interval, not a hard end-to-end revocation latency guarantee.
Exposure creation and each connection before dialing require fresh application
readiness. After dialing, before forwarding any payload, and during established
streams, the manager rechecks the complete runtime, process and endpoint identity
without sending another application health request. This permits single-threaded
HTTP servers to receive the waiting request. Identity-only success does not
refresh or overwrite stored readiness. Full readiness and identity observations
use separate gates; a concurrent application probe cannot block identity behind
the service lifecycle lock. Unavailable or changed identities fail closed.

`open_url` accepts an exposure ID/current generation and a constrained relative
path. The production service registry supplies a freshly checked loopback origin
and the executor verifies it again before dispatch. It does not accept an
arbitrary origin or another project's exposure.

The Host manager starts only for an approved dynamic exposure and survives the
launcher process. Read-only operations and ordinary Guest Bash do not start it.
Control uses a private owned Unix socket, instance nonce and full project binding.
Shutdown closes listeners, cancels work and joins in-flight observers before
exit. A new instance retains old records as unavailable; it never restores their
listeners or replays their consumed approvals. No PID file authorizes a kill.
Abandoned directory locks fail closed; no automatic stale-lock stealing exists.

## Budgets and actual network boundary

Per project: 32 retained services, 16 fixed publications, 16 retained dynamic
exposures; no automatic eviction. Each specification is at most 16 KiB; public
requests and complete MCP wire responses are at most 64 KiB. Over-budget responses
retain available resource/operation IDs for inspection instead of truncating data.
Private service state is capped at 4 MiB. Logs are at most 8 MiB and each read at
most 16 KiB; Docker tail and P3 cursor semantics remain distinct.

A manager admits at most 64 proxy streams, eight concurrent control requests,
16 KiB buffers in each direction, and a 30-second idle I/O timeout. IPv4 loopback
is the first supported listener family. Published loopback ports are not proof
of LAN isolation: OrbStack LAN forwarding, container IP/domain routing, VPN and
Guest-to-Host reachability are separate boundaries. This implementation does not
change global OrbStack networking, firewall, DNS or VPN settings. See the retained
real-network results and missing LAN-device acceptance in the P4 validation record.
