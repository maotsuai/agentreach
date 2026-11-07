# Explicit runtime recovery (P5)

The operator recovery path is separate from normal managed execution. A stopped,
restarted or missing runtime never weakens a session guard. Recovery verifies the
canonical Host control root, approved external volume identity/sentinel, engine
data location, fixed Docker connection and daemon before mutation. It cannot
execute arbitrary Host argv or grant Guest/service/browser permissions.

## Read, request, approve, apply

Read-only plans write JSON to stdout and create no state or Docker objects:

~~~sh
reach runtime reconcile --plan PROJECT
reach project migrate --plan PROJECT
reach runtime stop --plan PROJECT
reach runtime start --plan PROJECT
reach runtime restart --plan PROJECT
reach project rebind --plan PROJECT FULL_CONTAINER_ID
reach runtime orphan-stop --plan PROJECT FULL_CONTAINER_ID
reach runtime manager-stop --plan PROJECT RETIRED_RECORD_DIGEST
~~~

Review desired/observed, differences, retained identities, unresolved operations,
risks and the fixed effects set. Labels are inventory hints, never ownership
proof. Terminal/fixed-publication observations explicitly say not_probed;
reconciliation does not run a Guest helper or app-health request and does not
claim these retained objects are healthy.

Plans bind the complete expected project digest/revision, policy revision,
observations, native boot identity and monotonic deadline. Identical observations
in one ten-minute native clock window yield the same digest. Validity is the
**remainder** of that window, not a new lease on every read. A changed observation,
policy, binding, boot or expired window returns plan_stale. Obtain a new plan
and approval; old approval cannot authorize a recomputed plan.

Redirect a plan to an operator-controlled file outside Guest binds. Creating
that file is an explicit shell redirection, not a hidden plan mutation.

~~~sh
reach project migrate --plan PROJECT > reviewed-plan.json
reach runtime request PROJECT < reviewed-plan.json
reach runtime approve PROJECT REQUEST_ID EXACT_REQUEST_DIGEST
printf '%s\n' '{"request_id":"REQUEST_ID","approval_id":"APPROVAL_ID"}' |
  reach runtime apply PROJECT
~~~

Request retains the plan and creates a P2 pending request; it neither executes
nor approves. Approve accepts only requests owned by a retained runtime plan.
The ledger must require approval for runtime_change. If the current binding has
no ledger and the workspace is unavailable, `runtime policy PROJECT init`
creates a fresh policy with Guest and Host effects denied. Existing or corrupt
state is preserved. The dedicated command can change only the runtime recovery
rule. On a healthy binding, `reach policy set PROJECT guest allow` explicitly
enables ordinary Guest effects if needed.

~~~sh
reach runtime policy PROJECT init
reach runtime policy PROJECT approval
reach runtime policy PROJECT deny
~~~

Managed children and MCP callers cannot submit a recovery plan. In the v1
ledger the migration request uses the legacy operation encoding `restart` so
P4 can still read pending requests. Its exact effect is
`publish_project_schema_v2` and its digest binds the reviewed migration. P4 has
no runtime executor, and P5 dispatches the archived migration action.

## Schema v2 and authority

Project inspect remains read-only for both schemas. Migration is explicit;
v1 keeps revision/generation=1 and old binaries reject v2. Revision counts
authorization changes; generation counts changed execution identity. Migration
increments revision only; rebind increments both and pins full ID/StartedAt.

The projects/PROJECT.json file is the **only** current binding authority. CAS
checks the complete expected digest and revision under the project lock. The
recovery journal retains the original record, candidate, pending request and
original operation ID before publication/dispatch. Atomic project-record rename
is the binding commit point. Journal completion is not a second authority:
after interruption, compare the current digest to retained before/after records;
never choose by mtime or whichever file looks newest.

Each v2 record uses epochs/FULL_RECORD_DIGEST for policy, terminals, service
backends and service/exposure state. The original namespace, HMAC key, approvals
and security journal remain private and retained. A new session has a new name;
old session/child/object references are not redirected. Initialize policy and
create new sessions/objects explicitly; new Host permissions default to deny.

Old dynamic listeners and streams fail closed through their binding checks.
An idle original Host manager can then be retired with an approved manager-stop
plan using its private socket and exact instance nonce. For a running P4 binary,
the exact schema rejection of a nonce-bound query verifies the legacy peer.
The plan shows retained listener records and the P4 server refuses shutdown
while any live listener remains. An already dispatched Docker call can complete
across publication. Docker and the private Host record have separate commit
boundaries.

Stop/start/restart name exact IDs. Start/restart return a new **read-only rebind
plan**, not an automatic binding. Fixed subeffects recheck the consumed grant,
deadline, storage and identities. Stop can lose workspace process/terminal
memory; detached descendants and sidecars have separate lifecycles. No process
memory is restored and no input is replayed.

## Restricted provisioning

Runtime provision --plan reads a closed specification from stdin. It reuses
approved mounts and an existing project bridge/local image; named volume
identity must match. Retention is preserve. No build, pull, implicit bind
directory, new driver option, repository Compose, engine action or volume
removal is available.

The source is an exact approved stopped/created workspace, or the exact same
approved snapshot template retained in an earlier migration/rebind/provision
plan. The template contains only the fields already hashed by the workspace
snapshot and supports bounded json-file logging; no Guest argv/environment is
implicitly copied. A missing container can be provisioned from that retained
template, with all current storage/engine/image/network/volume checks repeated.
A missing original configuration **and** missing template is reported; use the
backup/restore runbook or a separately reviewed replacement. A checksum alone
cannot reconstruct an unknown Docker specification.

~~~json
{
  "schema": 1,
  "image": "sha256:FULL_LOCAL_IMAGE_DIGEST",
  "network_id": "FULL_EXISTING_PROJECT_NETWORK_ID",
  "user": "10001:10001",
  "argv": ["sleep", "infinity"],
  "env": {},
  "labels": {
    "dev.agentreach.managed": "true",
    "dev.agentreach.project": "PROJECT",
    "agentreach.task": "OPERATOR_TASK"
  },
  "memory": 536870912,
  "nano_cpus": 1000000000,
  "pids": 128,
  "log_bytes": 4194304,
  "log_size": "4m",
  "retention": "preserve"
}
~~~

Exact log option spelling is part of the reviewed configuration. The plan
publishes the exact created snapshot with `HostConfig.OomKillDisable=false`
and the second exact snapshot with `null`. Docker returned both default
representations in the native drill. The field is documented by the
[Docker Engine API](https://docs.docker.com/reference/api/engine/version/v1.44/).
The operator approves both full hashes. The existing binding retains its exact
raw snapshot. A created runtime must match the exact created snapshot
before start. A fresh observation after start must match one of the two
approved hashes and the exact new runtime identity. The full ID is saved
immediately after create, followed by a
durable confirmation of the typed command, environment and full snapshot.
A known provisioned ID requires that confirmation or an original successful
provisioning result to be eligible for rebind, together with the exact snapshot,
original intent and recorded Docker name.

A new container does not inherit old writable-rootfs changes. Prepare the
approved helper explicitly on the newly recorded exact resource, then apply a
separate rebind plan. Never install during health or ordinary dispatch.

## Unknown outcomes and orphan governance

P2 approval consumption and durable intent precede every effect. Interrupted
dispatch/result storage or unresolved intent retains the original operation ID.
Duplicate apply returns that identity without dispatch, including after
publication or under a new binding. The original P2 result governs if its
durable update succeeded before the recovery receipt could be written. An
unreadable ledger leaves a retained receipt ID available as unconfirmed. Inspect
the exact resource and review the original effect before requesting a new plan.

Sidecar orphan-stop requires its retained binding, service record, original P2
operation, complete effective configuration and StartedAt. Provisioned resources
require original intent, recorded full ID and pre-approved snapshot. Unknown
ownership is report-only. Cleanup stops the proven exact resource and preserves
volumes/data; it is not prune or remove-all.

Directory locks are bounded and never stolen. SIGKILL while holding one can
leave a lock. Read-only diagnosis preserves state; mutation reports the exact
locked path. Manual recovery requires:

1. Retain the original operation, journal and current project record. Inspect
   the actual resource before considering any new action.
2. Prove the holder exited using a retained process handle or equivalent OS
   evidence; identify the exact lock path and inode. Mtime, a reused PID or
   timeout alone is insufficient.
3. In an explicit operator window, archive that exact lock on the same private
   volume. Never rename a lock whose holder is uncertain.
4. Read a fresh plan and reconcile the original operation without replay.

This is an operator procedure, not transparent crash recovery. Corrupt records,
unsafe links/permissions, exhausted journals and unavailable storage fail
closed. They are never deleted or reinitialized to manufacture success.

See [Managed workspace backup and restore](BACKUP-RESTORE.md) for data recovery
in a separate project and [P5 validation](validation/P5.md) for exercised limits.
