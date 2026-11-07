#!/usr/bin/env python3
"""Recover only P5-owned exact runtimes after removal, retaining original data.

Preconditions: a completed independent backup and an exact approved template
already retained by the new controller. No remove-volume, global operation or
unknown operation replay is performed.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess


def main():
    ap = argparse.ArgumentParser()
    for k in ["root", "control", "reach", "helper"]:
        ap.add_argument("--" + k, required=True)
    a = ap.parse_args()
    root, home = Path(a.root), Path(a.control)
    fixtures = json.loads((root / "evidence/resources.json").read_text())
    backup = json.loads((root / "evidence/native-backup-restore.json").read_text())
    assert fixtures["task"] == "p5-20261107-01a1b418" and fixtures["state"] == str(home) and backup["complete"]
    path = root / "evidence/native-missing-runtime.json"
    assert not path.exists(), "Inspect exact current state before any follow-up"
    out = {"binary_sha256": hashlib.sha256(Path(a.reach).read_bytes()).hexdigest(), "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "calls": [], "modes": {}}
    env = dict(os.environ, REACH_HOME=str(home))
    docker = ["docker", "--context", "orbstack"]

    def save():
        path.write_text(json.dumps(out, indent=2))

    def run(argv, q=None, reach=True, okay=True):
        rec = {"argv": argv, "state": "dispatching"}
        if reach:
            rec["request"] = q
        out["calls"].append(rec)
        save()
        p = subprocess.run(([a.reach] + argv) if reach else argv, input=json.dumps(q) if q is not None else None,
                           capture_output=True, text=True, env=env, timeout=150)
        rec.update(exit=p.returncode, stdout=p.stdout, stderr=p.stderr, state="returned")
        save()
        assert (p.returncode == 0) == okay, rec
        return json.loads(p.stdout) if p.stdout.lstrip().startswith(("{", "[")) else p.stdout.strip()

    def apply(project, plan):
        assert plan["eligible"], plan
        request = run(["runtime", "request", project], plan)
        grant = run(["runtime", "approve", project, request["request_id"], request["request_digest"]])
        q = {"request_id": request["request_id"], "approval_id": grant["approval_id"]}
        operation = run(["runtime", "apply", project], q)
        repeated = run(["runtime", "apply", project], q, okay=False)
        assert repeated["operation"]["operation_id"] == operation["operation_id"]
        return operation

    for mode, fixture in fixtures["resources"].items():
        project = fixture["project"]
        original = run(["project", "inspect", project])
        old_id = original["binding"]["container_id"]
        actual = run(docker + ["inspect", old_id], reach=False)[0]
        assert actual["Id"] == old_id and actual["Config"]["Labels"]["agentreach.task"] == fixtures["task"]
        assert actual["Config"]["Labels"]["dev.agentreach.project"] == project
        data_manifest = json.loads(Path(backup["restored"][mode]["manifest"]).read_text())
        assert hashlib.sha256(Path(backup["restored"][mode]["backup"]).read_bytes()).hexdigest() == data_manifest["archive_sha256"]
        journal = json.loads((home / "runtime" / (project + ".json")).read_text())["payload"]
        assert any(entry.get("after", {}).get("binding", {}).get("container_id") == old_id and entry["plan"].get("approved_runtime_template") for entry in journal["entries"]), "No approved configuration template"
        state = {"original_id": old_id, "original_record": original, "backup_sha256": data_manifest["archive_sha256"]}
        out["modes"][mode] = state
        save()
        apply(project, run(["runtime", "stop", "--plan", project]))
        stopped = run(docker + ["inspect", old_id], reach=False)[0]
        assert stopped["State"]["Status"] == "exited" and not stopped["State"]["Running"]
        run(docker + ["rm", old_id], reach=False)  # exact verified task ID; no -v
        missing = run(["runtime", "reconcile", "--plan", project])
        assert missing["observed"]["bound"]["problem"] == "identity_changed", missing
        run(["--project", project, "exec", "--", "printf SHOULD_NOT_RUN"], okay=False)
        spec = {"schema": 1, "image": fixture["image"], "network_id": fixture["network_id"], "user": "10001:10001",
                "argv": ["sleep", "infinity"], "env": {}, "labels": {"agentreach.task": fixtures["task"], "dev.agentreach.managed": "true", "dev.agentreach.project": project},
                "memory": 512 << 20, "nano_cpus": 1000000000, "pids": 128, "log_bytes": 4 << 20, "log_size": "4m", "retention": "preserve"}
        plan = run(["runtime", "provision", "--plan", project], spec)
        assert plan["provision"]["configuration_source"] == "retained_approved_binding_template", plan
        created = apply(project, plan)
        new_id = created["created_container_id"]
        state["created_id"] = new_id
        save()
        actual = run(docker + ["inspect", new_id], reach=False)[0]
        assert new_id != old_id and actual["Id"] == new_id and actual["State"]["Running"]
        assert run(["project", "inspect", project])["binding"]["container_id"] == old_id
        for argv in [docker + ["cp", a.helper, new_id + ":/tmp/p5-reach-helper"],
                     docker + ["exec", "--user", str(os.getuid()), new_id, "/usr/bin/chmod", "0755", "/tmp/p5-reach-helper"],
                     docker + ["exec", "--user", "0", new_id, "/usr/bin/install", "-m", "0755", "/tmp/p5-reach-helper", "/usr/local/bin/reach-helper"]]:
            run(argv, reach=False)
        apply(project, created["rebind_plan"])
        rebound = run(["project", "inspect", project])
        assert rebound["binding"]["container_id"] == new_id and rebound["binding"]["generation"] == original["binding"]["generation"] + 1
        run(["policy", "init", project])
        for capability in ["runtime_change", "service_change", "port_expose", "open_url"]:
            run(["policy", "set", project, capability, "approval"])
        hashes = run(["--project", project, "exec", "--", "sha256sum data/payload.bin index.html"])
        assert data_manifest["entries"]["data/payload.bin"]["sha256"] in hashes
        assert data_manifest["entries"]["index.html"]["sha256"] in hashes
        state.update(data_checksums_preserved=True, used_retained_template=True, rebound_record=rebound, complete=True)
        save()

    out["complete"] = True
    save()
    print(json.dumps({"complete": True, "modes": list(out["modes"])}))


if __name__ == "__main__":
    main()
