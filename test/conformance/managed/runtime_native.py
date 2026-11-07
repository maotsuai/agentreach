#!/usr/bin/env python3
"""P5 native acceptance with write-ahead transcripts and exact fixture identities.

Each phase is single-use. Inspect an incomplete transcript and actual state
before choosing any follow-up; an unknown response is never a retry license.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import select
import socket
import subprocess
import time
import uuid


def main():
    ap = argparse.ArgumentParser()
    for k in ["root", "control", "reach", "helper"]:
        ap.add_argument("--" + k, required=True)
    ap.add_argument("--attempt", default="")
    ap.add_argument("--mode", choices=["shared","owned","both"],default="both")
    ap.add_argument("--phase", choices=["migration", "lifecycle", "provision", "orphans"], required=True)
    a = ap.parse_args()
    root, home = Path(a.root), Path(a.control)
    manifest = json.loads((root / "evidence/resources.json").read_text())
    assert manifest["task"] == "p5-20261107-01a1b418" and manifest["state"] == str(home)
    transcript = root / "evidence" / ("native-runtime-" + a.phase + ("-"+a.attempt if a.attempt else "") + ".json")
    assert not transcript.exists(), "Inspect previous outcomes before any follow-up"
    result = {"phase": a.phase, "binary_sha256": hashlib.sha256(Path(a.reach).read_bytes()).hexdigest(), "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "calls": [], "modes": {}}
    summary_path = root / "evidence/runtime-summary.json"
    summary = json.loads(summary_path.read_text()) if summary_path.exists() else {}
    env = dict(os.environ, REACH_HOME=str(home))
    docker = ["docker", "--context", "orbstack"]

    def save():
        transcript.write_text(json.dumps(result, indent=2))
        summary_path.write_text(json.dumps(summary, indent=2))

    def call(argv, q=None, okay=True, tool=None, custom_env=None):
        record = {"argv": argv, "request": q, "state": "dispatching"}
        result["calls"].append(record)
        save()
        p = subprocess.run(([tool or a.reach] + argv), input=json.dumps(q) if q is not None else None,
                           env=custom_env or env, capture_output=True, text=True, timeout=150)
        record.update(state="returned", exit=p.returncode, stdout=p.stdout, stderr=p.stderr)
        save()
        assert (p.returncode == 0) == okay, record
        return json.loads(p.stdout) if p.stdout and p.stdout.lstrip().startswith(("{", "[")) else {"output": p.stdout, "error": p.stderr}

    def inspect(cid):
        c = json.loads(subprocess.check_output(docker + ["inspect", cid]))[0]
        assert c["Id"] == cid and c["Config"]["Labels"]["agentreach.task"] == manifest["task"]
        return c

    def runtime_plan(project, action, target=None, spec=None):
        command = ["project" if action in ["migrate", "rebind"] else "runtime", action, "--plan", project]
        if target:
            command.append(target)
        return call(command, spec)

    def execute_plan(project, p):
        assert p["eligible"], p
        pending = call(["runtime", "request", project], p)
        approval = call(["runtime", "approve", project, pending["request_id"], pending["request_digest"]])
        q = {"request_id": pending["request_id"], "approval_id": approval["approval_id"]}
        operation = call(["runtime", "apply", project], q)
        assert operation["outcome"] == "ok" and operation["operation_id"]
        duplicate = call(["runtime", "apply", project], q, False)
        assert duplicate["operation"]["operation_id"] == operation["operation_id"], duplicate
        return operation

    def control_epoch(record):
        if record["spec"]["schema"] == 1:
            return home
        # Use the plan's canonical complete record digest, not Python's JSON formatting.
        p = runtime_plan(record["spec"]["id"], "reconcile")
        return home / "epochs" / p["expected_record_digest"]

    def initialize(project):
        call(["policy", "init", project])
        for cap in ["runtime_change", "service_change", "port_expose", "open_url"]:
            call(["policy", "set", project, cap, "approval"])
        call(["--project", project, "exec", "--", "printf P5_NEW_BINDING_READY"])

    def host(project, h):
        pending = call(["host", "plan", project], h)
        approval = call(["policy", "approve", project, pending["request_id"], pending["request_digest"]])
        return call(["host", "execute", project], {"request_id": pending["request_id"], "approval_id": approval["approval_id"]})

    def service(project, op, q):
        return call(["--project", project, "service", op], q)

    def wait_service(project, sid):
        for _ in range(25):
            s = service(project, "inspect", {"service_id": sid})["service"]
            if s["ready"]:
                return s
            time.sleep(.2)
        raise AssertionError(s)

    def workspace_service(project, fixture):
        program = "import socketserver\nclass H(socketserver.BaseRequestHandler):\n def handle(self):\n  while True:\n   b=self.request.recv(1024)\n   if not b: break\n   self.request.sendall(b)\nclass S(socketserver.ThreadingTCPServer): daemon_threads=True\nS.allow_reuse_address=True\nS(('0.0.0.0',8091),H).serve_forever()"
        spec = {"schema": 1, "project_id": project, "driver": "workspace", "argv": ["/usr/bin/python3", "-u", "-c", program],
                "cwd": "/workspace/project", "env": {}, "mounts": [], "network_id": fixture["network_id"], "user": "10001:10001",
                "resources": {"memory": 512 << 20, "nano_cpus": 1000000000, "pids": 128},
                "health": {"protocol": "tcp", "port": 8091, "timeout_ms": 1000}, "restart": "no", "log_bytes": 8 << 20,
                "guest_ports": [8091], "publish": []}
        s = service(project, "start", {"request_id": uuid.uuid4().hex, "spec": spec})["service"]
        return wait_service(project, s["service_id"])

    def sidecar(project, fixture):
        spec = {"schema": 1, "project_id": project, "driver": "sidecar", "image": fixture["image"],
                "argv": ["/usr/bin/python3", "-u", "-m", "http.server", "8080", "--bind", "0.0.0.0"],
                "cwd": "/tmp", "env": {}, "mounts": [], "network_id": fixture["network_id"], "user": "10001:10001",
                "resources": {"memory": 128 << 20, "nano_cpus": 500000000, "pids": 64}, "health": {"protocol": "http", "port": 8080, "path": "/", "timeout_ms": 1000},
                "restart": "no", "log_bytes": 1 << 20, "guest_ports": [8080], "publish": []}
        s = service(project, "plan", {"request_id": uuid.uuid4().hex, "spec": spec})["service"]
        p = s["pending"]
        approval = call(["policy", "approve", project, p["request_id"], p["request_digest"]])
        call(["host", "execute", project], {"request_id": p["request_id"], "approval_id": approval["approval_id"]})
        return wait_service(project, s["service_id"])

    def bridge_call(proc, seq, name, args):
        message = {"jsonrpc": "2.0", "id": seq, "method": "tools/call", "params": {"name": name, "arguments": args}}
        proc.stdin.write(json.dumps(message) + "\n")
        proc.stdin.flush()
        assert select.select([proc.stdout], [], [], 30)[0], "bridge response deadline"
        reply = json.loads(proc.stdout.readline())
        result.setdefault("bridge", []).append({"request": message, "response": reply})
        save()
        return reply

    for mode, fixture in manifest["resources"].items():
        if a.mode!="both" and mode!=a.mode:
            continue
        project = fixture["project"]
        data = summary.setdefault(mode, {"project": project})
        result["modes"][mode] = data
        record = call(["project", "inspect", project])
        cid = record["binding"]["container_id"]
        inspect(cid)

        if a.phase == "migration":
            assert record["spec"]["schema"] == 1
            old_plan = runtime_plan(project, "reconcile")
            old_digest = old_plan["expected_record_digest"]
            old_name = "project-" + project
            data.update(old_digest=old_digest, old_session=old_name, original_id=cid)
            tid = call(["--project", project, "terminal", "create"], {"request_id": uuid.uuid4().hex, "argv": ["/usr/bin/python3", "-q"], "cols": 80, "rows": 24})["terminal_id"]
            call(["--project", project, "terminal", "write"], {"terminal_id": tid, "input_id": uuid.uuid4().hex, "data": base64.b64encode(b"p5_old=41\r").decode()})
            data["old_terminal"] = tid
            s = workspace_service(project, fixture)
            data["old_workspace_service"] = s
            data["old_sidecar"] = sidecar(project, fixture)
            host(project, {"capability": "port_expose", "target_id": s["service_id"], "generation": s["generation"], "params": {"service_id": s["service_id"], "guest_port": 8091, "host_port": 0, "listen": "127.0.0.1"}})
            xs = call(["--project", project, "port", "list"], {})
            x = next(x for x in xs if x["state"] == "open")
            data["old_exposure"] = x
            active = socket.create_connection((x["address"], x["port"]), timeout=10)
            active.sendall(b"P5_ACTIVE_BEFORE_MIGRATION")
            assert active.recv(1024) == b"P5_ACTIVE_BEFORE_MIGRATION"
            pending = call(["host", "plan", project], {"capability": "open_url", "target_id": x["exposure_id"], "generation": x["target"]["generation"], "params": {"path": "/"}})
            grant = call(["policy", "approve", project, pending["request_id"], pending["request_digest"]])
            data["unused_old_approval"] = {"request_id": pending["request_id"], "approval_id": grant["approval_id"]}
            proc = subprocess.Popen([a.reach, "host-bridge", "--session", old_name], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
            try:
                healthy = bridge_call(proc, 1, "host_status", {})
                assert not healthy.get("result", {}).get("isError"), healthy
                old_key_hash = hashlib.sha256(bytes(json.loads((home / "security" / (project + ".json")).read_text())["ledger"]["hmac_key"], "utf8")).hexdigest()
                p = runtime_plan(project, "migrate")
                op = execute_plan(project, p)
                data["migration_operation"] = op
                after = call(["project", "inspect", project])
                assert after["spec"]["schema"] == after["binding"]["schema"] == 2
                assert after["binding"]["revision"] == 2 and after["binding"]["generation"] == 1
                assert after["binding"]["container_id"] == cid
                active.settimeout(15)
                try:
                    assert active.recv(1024) == b""
                except ConnectionResetError:
                    pass
                retired = bridge_call(proc, 2, "host_status", {})
                assert retired.get("result", {}).get("isError") or "error" in retired, retired
                again = bridge_call(proc, 3, "terminal_inspect", {"terminal_id": tid})
                assert again.get("result", {}).get("isError") or "error" in again, again
                oldenv = dict(env, REACH_MANAGED="1", REACH_MANAGED_PROJECT=project, REACH_MANAGED_RECORD=old_digest, REACH_SESSION=old_name)
                call(["exec", "--", "printf SHOULD_NOT_RUN"], okay=False, custom_env=oldenv)
                call(["exec", "--session", old_name, "--", "printf SHOULD_NOT_RUN"], okay=False)
                old_binary = "/Volumes/MPM/MCACHES/agentreach/p4-20261106-01a1b154/bin/reach-darwin-http-identity-final"
                call(["project", "inspect", project], okay=False, tool=old_binary)
                assert old_key_hash == hashlib.sha256(bytes(json.loads((home / "security" / (project + ".json")).read_text())["ledger"]["hmac_key"], "utf8")).hexdigest()
                initialize(project)
                call(["host", "execute", project], data["unused_old_approval"], False)
                call(["--project", project, "terminal", "inspect"], {"terminal_id": tid}, False)
                service(project, "list", {})
                call(["--project", project, "service", "inspect"], {"service_id": s["service_id"]}, False)
                call(["--project", project, "port", "inspect"], {"exposure_id": x["exposure_id"]}, False)
                data["migration_complete"] = True
            finally:
                active.close()
                proc.stdin.close()
                proc.wait(timeout=15)
            save()

        elif a.phase == "lifecycle":
            assert data["migration_complete"] and record["spec"]["schema"] == 2
            before_revision = record["binding"]["revision"]
            stopped = execute_plan(project, runtime_plan(project, "stop"))
            assert not inspect(cid)["State"]["Running"]
            call(["--project", project, "exec", "--", "printf SHOULD_NOT_RUN"], okay=False)
            started = execute_plan(project, runtime_plan(project, "start"))
            assert started["rebind_required"] and started["rebind_plan"]["eligible"]
            assert call(["project", "inspect", project])["binding"] == record["binding"]
            call(["--project", project, "exec", "--", "printf SHOULD_NOT_RUN"], okay=False)
            execute_plan(project, started["rebind_plan"])
            rebound = call(["project", "inspect", project])
            assert rebound["binding"]["generation"] == record["binding"]["generation"] + 1
            initialize(project)
            restarted = execute_plan(project, runtime_plan(project, "restart"))
            assert restarted["rebind_required"] and restarted["rebind_plan"]["eligible"]
            assert call(["project", "inspect", project])["binding"] == rebound["binding"]
            execute_plan(project, restarted["rebind_plan"])
            newest = call(["project", "inspect", project])
            assert newest["binding"]["revision"] == before_revision + 2
            initialize(project)
            data["lifecycle_complete"] = True
            save()

        elif a.phase == "provision":
            assert data["lifecycle_complete"]
            if inspect(cid)["State"]["Running"]:
                execute_plan(project, runtime_plan(project, "stop"))
            launch = {"schema": 1, "image": fixture["image"], "network_id": fixture["network_id"], "user": "10001:10001", "argv": ["sleep", "infinity"], "env": {},
                      "labels": {"agentreach.task": manifest["task"], "dev.agentreach.managed": "true", "dev.agentreach.project": project},
                      "memory": 512 << 20, "nano_cpus": 1000000000, "pids": 128, "log_bytes": 4 << 20, "log_size": "4m", "retention": "preserve"}
            provisioned = execute_plan(project, runtime_plan(project, "provision", spec=launch))
            new_id = provisioned["created_container_id"]
            new = inspect(new_id)
            assert new["State"]["Running"] and new_id != cid
            assert call(["project", "inspect", project])["binding"]["container_id"] == cid
            # Explicit helper preparation on this newly created exact fixture.
            for argv in [docker + ["cp", a.helper, new_id + ":/tmp/p5-reach-helper"],
                         docker + ["exec", "--user", str(os.getuid()), new_id, "/usr/bin/chmod", "0755", "/tmp/p5-reach-helper"],
                         docker + ["exec", "--user", "0", new_id, "/usr/bin/install", "-m", "0755", "/tmp/p5-reach-helper", "/usr/local/bin/reach-helper"]]:
                p = subprocess.run(argv, capture_output=True, text=True, timeout=30)
                result.setdefault("operator_preparation", []).append({"argv": argv, "exit": p.returncode, "stderr": p.stderr})
                save()
                assert p.returncode == 0
            execute_plan(project, provisioned["rebind_plan"])
            newest = call(["project", "inspect", project])
            assert newest["binding"]["container_id"] == new_id and newest["binding"]["generation"] == record["binding"]["generation"] + 1
            initialize(project)
            data["provisioned_id"] = new_id
            data["provision_complete"] = True
            save()

        elif a.phase == "orphans":
            assert data["migration_complete"]
            target = data["old_sidecar"]["container_id"]
            # Sidecar labels lack fixture task label; prove its original exact service label.
            physical = json.loads(subprocess.check_output(docker + ["inspect", target]))[0]
            assert physical["Id"] == target and physical["Config"]["Labels"]["dev.agentreach.service"] == data["old_sidecar"]["service_id"]
            p = runtime_plan(project, "orphan-stop", target)
            assert p["ownership"]["ownership_verified"], p
            execute_plan(project, p)
            assert not json.loads(subprocess.check_output(docker + ["inspect", target]))[0]["State"]["Running"]
            missing = runtime_plan(project, "orphan-stop", "f" * 64)
            assert not missing["eligible"] and not missing["ownership"]["ownership_verified"]
            call(["runtime", "request", project], missing, False)
            manager_plan = runtime_plan(project, "manager-stop", data["old_digest"])
            assert manager_plan["retired_manager"]["private_manager_reachable"], manager_plan
            execute_plan(project, manager_plan)
            data["orphans_complete"] = True
            save()

    result["complete"] = True
    save()
    print(json.dumps({"phase": a.phase, "complete": True, "modes": list(result["modes"])}))


if __name__ == "__main__":
    main()
