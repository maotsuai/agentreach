#!/usr/bin/env python3
"""P5 read-only native proof. Uses only explicitly supplied P5 fixture identities."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time


def main():
    p = argparse.ArgumentParser()
    for k in ["root", "control", "reach"]:
        p.add_argument("--" + k, required=True)
    p.add_argument("--attempt", default="")
    a = p.parse_args()
    root, control = Path(a.root), Path(a.control)
    manifest = json.loads((root / "evidence/resources.json").read_text())
    assert manifest["task"] == "p5-20261107-01a1b418" and manifest["complete"]
    assert manifest["state"] == str(control)
    ids = [r["id"] for r in manifest["resources"].values()]
    projects = [r["project"] for r in manifest["resources"].values()]
    docker = ["docker", "--context", "orbstack"]
    out = {"binary_sha256": hashlib.sha256(Path(a.reach).read_bytes()).hexdigest(), "cases": []}

    def call(argv, home=control):
        p = subprocess.run(argv, capture_output=True, text=True, timeout=50, env=dict(os.environ, REACH_HOME=str(home)))
        return {"argv": argv, "exit": p.returncode, "stdout": p.stdout, "stderr": p.stderr}

    for project in projects:
        inventory = call(docker + ["container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=dev.agentreach.project=" + project])
        assert inventory["exit"] == 0, inventory
        ids.extend(inventory["stdout"].split())
    ids = sorted(set(ids))

    unrelated_epochs = set()
    backup = root / "evidence/native-backup-restore.json"
    if backup.exists():
        restored = json.loads(backup.read_text())
        assert restored["complete"]
        for fixture in restored["restored"].values():
            project = fixture["project"]
            assert project.startswith("p5-restore-") and project not in projects
            report = call([a.reach, "runtime", "reconcile", "--plan", project])
            assert report["exit"] == 0, report["stderr"]
            view = json.loads(report["stdout"])
            digest = view["expected_record_digest"]
            assert len(digest) == 64 and all(c in "0123456789abcdef" for c in digest)
            unrelated_epochs.add(control / "epochs" / digest)
    out["excluded_unrelated_active_restore_epochs"] = sorted(str(p.relative_to(control)) for p in unrelated_epochs)

    def files(home):
        if not home.exists():
            return {}
        values = {}
        paths = [home]
        for f in paths:
            st = f.lstat()
            assert not f.is_symlink()
            values[str(f.relative_to(home))] = [st.st_mode, st.st_mtime_ns, hashlib.sha256(f.read_bytes()).hexdigest() if f.is_file() else None]
            if f.is_dir():
                paths.extend(child for child in sorted(f.iterdir()) if child not in unrelated_epochs)
        return values

    def resources():
        r = call(docker + ["inspect", *ids])
        assert r["exit"] == 0
        full = json.loads(r["stdout"])
        for c in full:
            assert c["Id"] in ids and c["Config"]["Labels"]["dev.agentreach.project"] in projects
        return full

    def changed_paths(before, after, prefix=""):
        if type(before) is not type(after):
            return [prefix]
        if isinstance(before, dict):
            return [
                path
                for key in sorted(before.keys() | after.keys())
                for path in ([f"{prefix}.{key}"] if key not in before or key not in after
                             else changed_paths(before[key], after[key], f"{prefix}.{key}"))
            ]
        if isinstance(before, list):
            if len(before) != len(after):
                return [prefix + ".length"]
            return [path for i, (x, y) in enumerate(zip(before, after))
                    for path in changed_paths(x, y, f"{prefix}[{i}]")]
        return [] if before == after else [prefix]

    since = str(int(time.time()))
    for mode, fixture in manifest["resources"].items():
        before_files, before_resources = files(control), resources()
        first = call([a.reach, "runtime", "reconcile", "--plan", fixture["project"]])
        second = call([a.reach, "runtime", "reconcile", "--plan", fixture["project"]])
        assert first["exit"] == second["exit"] == 0, [first, second]
        x, y = json.loads(first["stdout"]), json.loads(second["stdout"])
        assert x == y and x["effects"] == [] and not x["eligible"]
        assert x["observed"]["engine"] == "verified" and x["observed"]["bound"]["matches_registered_identity"]
        after_files, after_resources = files(control), resources()
        delta = {
            "control_paths": changed_paths(before_files, after_files, "control"),
            "docker_paths": changed_paths(before_resources, after_resources, "containers"),
        }
        assert not any(delta.values()), delta
        out["cases"].append({"mode": mode, "plan": x, "files_unchanged": True, "docker_objects_unchanged": True})

    diagnostics = control.parent / ("plan-diagnostics" + ("-" + a.attempt if a.attempt else ""))
    assert not diagnostics.exists()
    diagnostics.mkdir(mode=0o700)
    fixture = manifest["resources"]["shared"]
    for case in ["missing", "corrupt"]:
        home = diagnostics / case
        if case == "corrupt":
            (home / "projects").mkdir(mode=0o700, parents=True)
            home.chmod(0o700)
            shutil.copyfile(control / "projects" / (fixture["project"] + ".json"), home / "projects" / (fixture["project"] + ".json"))
            (home / "projects" / (fixture["project"] + ".json")).chmod(0o600)
            project_record = json.loads((home / "projects" / (fixture["project"] + ".json")).read_text())
            epoch = home
            if project_record["spec"]["schema"] == 2:
                epoch = home / "epochs"
                epoch.mkdir(mode=0o700)
                epoch = epoch / out["cases"][0]["plan"]["expected_record_digest"]
                epoch.mkdir(mode=0o700)
            (epoch / "security").mkdir(mode=0o700)
            f = epoch / "security" / (fixture["project"] + ".json")
            f.write_bytes(b'{"corrupt":')
            f.chmod(0o600)
        before = files(diagnostics)
        r = call([a.reach, "runtime", "reconcile", "--plan", fixture["project"]], home)
        if case == "missing":
            assert r["exit"] != 0 and not home.exists(), r
        else:
            assert r["exit"] == 0, r
            v = json.loads(r["stdout"])
            assert v["control"]["problems"] and v["effects"] == [] and not v["eligible"]
        assert files(diagnostics) == before
        out["cases"].append({"diagnostic": case, "result": r, "files_unchanged": True})

    until = str(int(time.time()) + 1)
    for cid in ids:
        events = call(docker + ["events", "--since", since, "--until", until, "--filter", "container=" + cid, "--format", "{{json .}}"])
        assert events["exit"] == 0 and events["stdout"] == "", events
    out["no_container_events"] = True
    out["complete"] = True
    (root / ("evidence/readonly-plan" + ("-" + a.attempt if a.attempt else "") + ".json")).write_text(json.dumps(out, indent=2))
    print(json.dumps({"complete": True, "cases": len(out["cases"]), "no_container_events": True}))


if __name__ == "__main__":
    main()
