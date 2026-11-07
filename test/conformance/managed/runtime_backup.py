#!/usr/bin/env python3
"""P5 data-only backup/restore drill into fresh, recorded Guest resources.

Controls/HMAC/model credentials are never archived. No Host tar extraction,
raw disk edits, original volume replacement, pull, prune or implicit cleanup.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import selectors
import subprocess
import time
import urllib.request
import uuid
from runtime_archive import MAX_ARCHIVE, normalize_for_guest, validate_archive


def main():
    ap = argparse.ArgumentParser()
    for k in ["root", "control", "reach", "helper"]:
        ap.add_argument("--" + k, required=True)
    a = ap.parse_args()
    root, home = Path(a.root), Path(a.control)
    resource_manifest = json.loads((root / "evidence/resources.json").read_text())
    assert resource_manifest["task"] == "p5-20261107-01a1b418" and resource_manifest["state"] == str(home)
    output = root / "evidence/native-backup-restore.json"
    assert not output.exists(), "Inspect actual resources and unknown results before any follow-up"
    out = {"reach_sha256": hashlib.sha256(Path(a.reach).read_bytes()).hexdigest(), "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "calls": [], "restored": {}}
    env = dict(os.environ, REACH_HOME=str(home))
    docker = ["docker", "--context", "orbstack"]

    def save():
        output.write_text(json.dumps(out, indent=2))

    def run(argv, stdin=None, binary=False):
        rec = {"argv": argv, "state": "dispatching"}
        out["calls"].append(rec)
        save()
        p = subprocess.run(argv, input=stdin, capture_output=True, text=not binary, env=env, timeout=150)
        if binary:
            rec.update(exit=p.returncode, stdout_bytes=len(p.stdout), stderr=p.stderr.decode(errors="replace"), state="returned")
        else:
            rec.update(exit=p.returncode, stdout=p.stdout, stderr=p.stderr, state="returned")
        save()
        assert p.returncode == 0, rec
        return p.stdout

    def reach(argv, q=None):
        raw = run([a.reach] + argv, json.dumps(q) if q is not None else None)
        return json.loads(raw) if raw.lstrip().startswith(("{", "[")) else raw

    def execute_plan(project, plan):
        assert plan["eligible"], plan
        pending = reach(["runtime", "request", project], plan)
        grant = reach(["runtime", "approve", project, pending["request_id"], pending["request_digest"]])
        return reach(["runtime", "apply", project], {"request_id": pending["request_id"], "approval_id": grant["approval_id"]})

    def inspect(cid, project):
        c = json.loads(run(docker + ["inspect", cid]))[0]
        assert c["Id"] == cid and c["Config"]["Labels"]["dev.agentreach.project"] == project and c["Config"]["Labels"]["agentreach.task"] == resource_manifest["task"]
        return c

    def export(cid):
        command = docker + ["cp", cid + ":/workspace/project/.", "-"]
        rec = {"argv": command, "state": "dispatching", "stdout_budget": MAX_ARCHIVE}
        out["calls"].append(rec)
        save()
        proc = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        stdout, stderr = bytearray(), bytearray()
        sel = selectors.DefaultSelector()
        sel.register(proc.stdout, selectors.EVENT_READ, "stdout")
        sel.register(proc.stderr, selectors.EVENT_READ, "stderr")
        deadline = time.monotonic() + 60
        try:
            while sel.get_map():
                assert time.monotonic() < deadline, "archive command deadline"
                for key, _ in sel.select(.5):
                    chunk = os.read(key.fd, 65536)
                    if not chunk:
                        sel.unregister(key.fileobj)
                        continue
                    buf = stdout if key.data == "stdout" else stderr
                    buf.extend(chunk)
                    assert len(buf) <= (MAX_ARCHIVE if key.data == "stdout" else 32768), "archive command budget"
            code = proc.wait(timeout=5)
            rec.update(exit=code, stdout_bytes=len(stdout), stdout_sha256=hashlib.sha256(stdout).hexdigest(), stderr=stderr.decode(errors="replace"), state="returned")
            save()
            assert code == 0, rec
            return bytes(stdout)
        finally:
            sel.close()
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=5)
            proc.stdout.close()
            proc.stderr.close()

    for mode, fixture in resource_manifest["resources"].items():
        source_project = fixture["project"]
        source_record = reach(["project", "inspect", source_project])
        cid = source_record["binding"]["container_id"]
        inspect(cid, source_project)
        marker = "P5_RESTORED_" + mode + "_" + uuid.uuid4().hex
        program = "python3 - <<'PY'\nfrom pathlib import Path\nimport hashlib,json\np=Path('/workspace/project');(p/'data').mkdir(exist_ok=True)\n(p/'index.html').write_text(" + repr(marker) + ")\n(p/'data'/'payload.bin').write_bytes(bytes(range(256))*1025)\n(p/'data'/'unicode-恢复.txt').write_text('independent restore\\n')\nPY"
        reach(["--project", source_project, "exec", "--", program])
        stopped = execute_plan(source_project, reach(["runtime", "stop", "--plan", source_project]))
        assert not inspect(cid, source_project)["State"]["Running"]
        before = reach(["project", "inspect", source_project])
        data = export(cid)
        manifest = validate_archive(data)
        assert "index.html" in manifest["entries"] and manifest["entries"]["data/payload.bin"]["bytes"] == 256 * 1025, manifest
        backup = root / "evidence" / ("data-backup-" + mode + ".tar")
        with backup.open("xb") as f:
            f.write(data)
        assert hashlib.sha256(backup.read_bytes()).hexdigest() == manifest["archive_sha256"]
        reference = reach(["runtime", "reconcile", "--plan", source_project])
        manifest.update(control_reference={"record_digest": reference["expected_record_digest"], "revision": before["binding"]["revision"], "generation": before["binding"]["generation"]}, source_project_id=source_project, source_container_id=cid, source_started_at=source_record["binding"]["started_at"],
                        consistency="workspace container stopped after explicit approved stop; no database process/dump in this fixture",
                        control_state_included=False, credentials_included=False, retention="preserve_original",
                        restore_ownership="normalize uid/gid to 10001; directories0755/files0644; data checksums unchanged")
        manifest_file = backup.with_suffix(".manifest.json")
        with manifest_file.open("x") as f:
            json.dump(manifest, f, indent=2)
        normalized = normalize_for_guest(data)

        project = "p5-restore-" + uuid.uuid4().hex[:16]
        rec = {"source_project": source_project, "source_container": cid, "backup": str(backup), "manifest": str(manifest_file), "project": project}
        out["restored"][mode] = rec
        save()
        labels = ["--label", "agentreach.task=" + resource_manifest["task"], "--label", "dev.agentreach.managed=true", "--label", "dev.agentreach.project=" + project]
        network = run(docker + ["network", "create", "--driver", "bridge"] + labels + ["reach-" + project]).strip()
        rec["network_id"] = network
        save()
        volume = run(docker + ["volume", "create"] + labels + ["reach-" + project]).strip()
        rec["volume"] = volume
        save()
        destination = run(docker + ["create", "--name", "reach-" + project, "--network", network, "--user", "10001:10001", "--workdir", "/workspace/project",
                                    "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--init", "--restart", "no",
                                    "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "128", "--log-opt", "max-size=4m", "--log-opt", "max-file=1"] +
                          labels + ["--mount", "type=volume,src=" + volume + ",dst=/workspace/project,volume-nocopy", fixture["image"], "sleep", "infinity"]).strip()
        rec["container_id"] = destination
        save()
        assert destination != cid
        inspect(destination, project)
        # The validated archive is only streamed into a fresh empty Guest mount.
        run(docker + ["cp", "--archive", "-", destination + ":/workspace/project"], normalized, binary=True)
        restored_data = export(destination)
        restored_manifest = validate_archive(restored_data)
        assert restored_manifest["entries"] == manifest["entries"]
        rec["data_checksums_match"] = True
        run(docker + ["start", destination])
        for command in [docker + ["cp", a.helper, destination + ":/tmp/p5-reach-helper"],
                        docker + ["exec", "--user", str(os.getuid()), destination, "/usr/bin/chmod", "0755", "/tmp/p5-reach-helper"],
                        docker + ["exec", "--user", "0", destination, "/usr/bin/install", "-m", "0755", "/tmp/p5-reach-helper", "/usr/local/bin/reach-helper"]]:
            run(command)
        spec = dict(source_record["spec"], schema=2, id=project, container=destination, workspace={"mode": "guest-owned", "guest_path": "/workspace/project"},
                    bind_mounts=[], named_volumes=[{"name": volume, "target": "/workspace/project", "read_only": False}])
        specfile = home.parent / (project + ".json")
        with specfile.open("x") as f:
            json.dump(spec, f)
        specfile.chmod(0o600)
        reach(["project", "register", str(specfile)])
        reach(["policy", "init", project])
        reach(["policy", "set", project, "port_expose", "approval"])
        reach(["--project", project, "exec", "--", "printf RESTORE_WRITE_VERIFIED > data/new-binding.txt"])
        spec = {"schema": 1, "project_id": project, "driver": "workspace", "argv": ["/usr/bin/python3", "-u", "-m", "http.server", "8093", "--bind", "0.0.0.0", "--directory", "/workspace/project"],
                "cwd": "/workspace/project", "env": {}, "mounts": [], "network_id": network, "user": "10001:10001",
                "resources": {"memory": 512 << 20, "nano_cpus": 1000000000, "pids": 128}, "health": {"protocol": "http", "port": 8093, "path": "/", "timeout_ms": 1000},
                "restart": "no", "log_bytes": 8 << 20, "guest_ports": [8093], "publish": []}
        service = reach(["--project", project, "service", "start"], {"request_id": uuid.uuid4().hex, "spec": spec})["service"]
        for _ in range(30):
            service = reach(["--project", project, "service", "inspect"], {"service_id": service["service_id"]})["service"]
            if service["ready"]:
                break
            time.sleep(.2)
        assert service["ready"]
        pending = reach(["host", "plan", project], {"capability": "port_expose", "target_id": service["service_id"], "generation": service["generation"],
                        "params": {"service_id": service["service_id"], "guest_port": 8093, "host_port": 0, "listen": "127.0.0.1"}})
        grant = reach(["policy", "approve", project, pending["request_id"], pending["request_digest"]])
        reach(["host", "execute", project], {"request_id": pending["request_id"], "approval_id": grant["approval_id"]})
        exposure = next(x for x in reach(["--project", project, "port", "list"], {}) if x["state"] == "open")
        rec.update(service_id=service["service_id"], exposure_id=exposure["exposure_id"], listen_port=exposure["port"])
        save()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open("http://127.0.0.1:" + str(exposure["port"]) + "/index.html", timeout=10) as response:
            body = response.read()
            assert response.status == 200 and body == marker.encode()
        rec.update(http_status=200, http_body_sha256=hashlib.sha256(body).hexdigest(), marker_sha256=hashlib.sha256(marker.encode()).hexdigest())
        assert reach(["project", "inspect", source_project]) == before
        assert not inspect(cid, source_project)["State"]["Running"]
        started = execute_plan(source_project, reach(["runtime", "start", "--plan", source_project]))
        execute_plan(source_project, started["rebind_plan"])
        reach(["policy", "init", source_project])
        for cap in ["runtime_change", "service_change", "port_expose", "open_url"]:
            reach(["policy", "set", source_project, cap, "approval"])
        reach(["--project", source_project, "exec", "--", "printf SOURCE_PRESERVED"])
        rec["source_preserved"] = rec["complete"] = True
        save()
    out["complete"] = True
    save()
    print(json.dumps({"complete": True, "restored": list(out["restored"])}))


if __name__ == "__main__":
    main()
