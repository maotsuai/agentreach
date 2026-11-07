# Managed workspace backup and restore

## Choose a consistent data snapshot

1. Record the project ID, complete project record digest, revision, generation,
   full container ID and StartedAt. Verify the approved volume and daemon.
   Inventory shared bind paths and named volumes, and identify every writer.
2. Back up committed source in Git. Review uncommitted, untracked and ignored
   files separately, and retain the files required for recovery in a protected
   archive. Exclude credentials, configuration with secrets and control state.
3. For a database, use its version-compatible logical dump and verification
   commands. Quiesce application writers or use the database's consistent dump
   mechanism. Record the engine version, restore ordering and data checks.
   Stopping the workspace alone does not quiesce Host, sidecar or external
   writers to a shared directory or volume.
4. Stop the source with an approved exact-ID recovery plan when required.
   Verify the resulting state and retain the source binding and data. For an
   already quiesced file workspace, fixed-context Docker can stream the
   approved root from a stopped exact container. For example, run
   `docker --context orbstack cp FULL_ID:/workspace/project/. -` with a
   bounded reader and a backup location outside the project bind.

Record an archive manifest with source identity, consistency procedure, archive
SHA-256, byte count, relative paths and per-file SHA-256. Retain the archive
and independently protected manifest together. Verify coverage of all intended
data roots. The P5 drill uses synthetic files with a stopped source and no
other writers or database.

## Keep controls separate

Keep control evidence on the private Host volume outside Guest mounts. The
default data backup excludes `REACH_HOME` and model credentials. Exclude model
credentials from any control backup. If retention requires an archive containing
private ledger keys or other sensitive state, use an approved encrypted
destination, separate access to decryption keys and owner-only permissions
when validating and restoring it. Record the schema, binary and project
identity. Retained ledgers can explain original operations. A new project
requires its own approvals.

Keep independent archives of data and any required controls. Recovery does not
depend on an OrbStack raw engine disk image or editing it.

## Restore in isolation

1. Compare the full archive SHA-256 against the independent manifest. Validate
   before extraction. The P5 validator accepts bounded uncompressed tar with
   regular files and directories. It rejects traversal, absolute paths, links,
   special files, sparse entries, duplicate paths and unsupported metadata.
   Limits are 64 MiB of archive, 48 MiB of regular data and 4,096 entries.
2. Create and inspect a new empty volume, bridge and container with random
   names. Use a verified local image and the exact new container ID. Stream the
   normalized archive into the new empty Guest mount. The P5 drill normalizes
   UID/GID to 10001 and modes to 0755 for directories and 0644 for files.
   Executable, ACL-sensitive, ownership-sensitive or larger datasets require a
   separately reviewed procedure.
3. Re-export the destination and compare path, type and file checksums. Prepare
   the approved helper explicitly, start the new container, register the new
   project and initialize policy. If applicable, load and verify the logical
   dump. Confirm the registered user can read and write the intended data and
   that a newly approved service returns the expected content on its route.
4. Retain source and destination identities and test results. Create new
   sessions, terminals, services and approvals for the new binding. Old
   terminal memory is unavailable.

The [fixture runner](../test/conformance/managed/runtime_backup.py) writes
synthetic data before export. It is for dedicated P5 fixtures only. Use the
steps above for real project data.
