#!/usr/bin/env python3
"""P4 real macOS CLI acceptance, using only explicit task-owned resources.

Each phase has a write-ahead transcript and refuses automatic re-execution.
No model requests, pull/build, global changes, name-based cleanup or PID kills.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    for key in ['reach', 'resources', 'evidence']:
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--attempt', default='', help='new named attempt after inspecting and retiring prior failed listeners; never reuses original operations')
    parser.add_argument('--phase', choices=['services', 'workspace', 'proxy', 'manager', 'stop'], required=True)
    parser.add_argument('--mode', choices=['shared', 'owned', 'both'], default='both')
    args = parser.parse_args()
    manifest = json.loads(Path(args.resources).read_text())
    evidence = Path(args.evidence)
    evidence.mkdir(exist_ok=True)
    assert not args.attempt or args.attempt.replace('-', '').isalnum()
    transcript_file = evidence / ('native-' + args.phase + ('-' + args.attempt if args.attempt else '') + '.json')
    assert not transcript_file.exists(), 'Inspect retained outcomes; never automatically replay a phase'
    summary_file = evidence / 'native-summary.json'
    summary = json.loads(summary_file.read_text()) if summary_file.exists() else {}
    env = dict(os.environ, REACH_HOME=manifest['state'])
    transcript = {'phase': args.phase, 'reach_sha256': hashlib.sha256(Path(args.reach).read_bytes()).hexdigest(), 'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), 'calls': []}
    lock = threading.Lock()

    def save():
        transcript_file.write_text(json.dumps(transcript, indent=2))
        summary_file.write_text(json.dumps(summary, indent=2))

    def call(argv, q=None, okay=True):
        record = {'argv': argv, 'request': q, 'state': 'dispatching'}
        with lock:
            transcript['calls'].append(record)
            save()
        p = subprocess.run([args.reach] + argv, input=json.dumps(q) if q is not None else None, env=env, capture_output=True, text=True, timeout=45)
        with lock:
            record.update(state='returned', exit=p.returncode, stdout=p.stdout, stderr=p.stderr)
            save()
        assert (p.returncode == 0) == okay, record
        return json.loads(p.stdout) if p.stdout else {'error': p.stderr}

    def resource(mode):
        r = manifest['resources'][mode]
        c = json.loads(subprocess.check_output(['docker', '--context', 'orbstack', 'inspect', r['id']]))[0]
        assert c['Id'] == r['id'] and c['Config']['Labels']['agentreach.task'] == manifest['task']
        return r

    def service(mode, op, q, okay=True):
        return call(['--project', resource(mode)['project'], 'service', op], q, okay)

    def plan(mode, h):
        project = resource(mode)['project']
        p = call(['host', 'plan', project], h)
        a = call(['policy', 'approve', project, p['request_id'], p['request_digest']])
        return p, a

    def execute(mode, p, a, okay=True):
        return call(['host', 'execute', resource(mode)['project']], {'request_id': p['request_id'], 'approval_id': a['approval_id']}, okay)

    def ready(mode, sid):
        deadline = time.monotonic() + 15
        while True:
            s = service(mode, 'inspect', {'service_id': sid})['service']
            if s['ready']:
                return s
            assert time.monotonic() < deadline, s
            time.sleep(.05)

    def spec(mode, driver):
        r = resource(mode)
        s = {'schema': 1, 'project_id': r['project'], 'driver': driver, 'argv': ['/usr/bin/python3', '-u', '-m', 'http.server', '8080', '--bind', '0.0.0.0'], 'cwd': '/tmp', 'env': {}, 'mounts': [], 'network_id': r['network_id'], 'user': '10001:10001', 'resources': {'memory': 128 << 20, 'nano_cpus': 500000000, 'pids': 64}, 'health': {'protocol': 'http', 'port': 8080, 'path': '/', 'timeout_ms': 1000}, 'restart': 'no', 'log_bytes': 1 << 20, 'guest_ports': [8080], 'publish': []}
        if driver == 'sidecar':
            s['image'] = r['image']
            s['publish'] = [{'guest_port': 8080, 'host_port': 48145 if mode == 'shared' else 48146, 'listen': '127.0.0.1'}]
        else:
            program = "import socketserver\nclass H(socketserver.BaseRequestHandler):\n def handle(self):\n  self.request.settimeout(15); b=b''\n  while True:\n   x=self.request.recv(16384)\n   if not x: break\n   b+=x\n   if len(b)>1048576: return\n  try: self.request.sendall(b'P4_REPLY:'+b)\n  except OSError: pass\nclass S(socketserver.ThreadingTCPServer): daemon_threads=True\ns=S(('0.0.0.0',8091),H); print('P4_SERVICE_READY',flush=True); s.serve_forever()"
            s.update(argv=['/usr/bin/python3', '-u', '-c', program], cwd='/workspace/project', env={'P4_VALUE': 'literal$VALUE;noHost'}, resources={'memory': 512 << 20, 'nano_cpus': 1000000000, 'pids': 128}, health={'protocol': 'tcp', 'port': 8091, 'timeout_ms': 1000}, log_bytes=8 << 20, guest_ports=[8091])
        return s

    def half_close(x):
        data = bytes(range(256)) * 1024
        with socket.create_connection((x['address'], x['port']), timeout=10) as c:
            c.sendall(data)
            c.shutdown(socket.SHUT_WR)
            chunks = []
            while True:
                b = c.recv(16384)
                if not b:
                    break
                chunks.append(b)
        result = b''.join(chunks)
        assert result == b'P4_REPLY:' + data, (len(result), x)
        transcript.setdefault('streams', []).append({'exposure': x['exposure_id'], 'bytes': len(result), 'sha256': hashlib.sha256(result).hexdigest(), 'half_close': True})
        save()

    def ports(mode):
        return call(['--project', resource(mode)['project'], 'port', 'list'], {})

    def expose_plan(mode, port=0):
        s = ready(mode, summary[mode]['workspace']['service_id'])
        return plan(mode, {'capability': 'port_expose', 'target_id': s['service_id'], 'generation': s['generation'], 'params': {'service_id': s['service_id'], 'guest_port': 8091, 'host_port': port, 'listen': '127.0.0.1'}})

    def close_exposure(mode, x):
        p, a = plan(mode, {'capability': 'port_expose', 'target_id': x['exposure_id'], 'generation': x['target']['generation'], 'params': {'operation': 'close', 'service_id': x['target']['service_id'], 'listen': '127.0.0.1'}})
        execute(mode, p, a)
        with socket.socket() as c:
            c.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            c.bind((x['address'], x['port']))
            c.listen()

    def session_name(mode):
        matches = []
        for path in (Path(manifest['state']) / 'sessions').glob('*.json'):
            s = json.loads(path.read_text())
            if s.get('managed', {}).get('project_id') == resource(mode)['project']:
                matches.append(s['name'])
        assert len(matches) == 1, matches
        return matches[0]

    if args.phase == 'services':
        retained = json.loads((evidence / 'native-restart.json').read_text())['service']
        summary['shared'] = {'sidecar': ready('shared', retained['service_id'])}
        s = spec('owned', 'sidecar')
        p = service('owned', 'plan', {'request_id': uuid.uuid4().hex, 'spec': s})['service']
        grant = call(['policy', 'approve', resource('owned')['project'], p['pending']['request_id'], p['pending']['request_digest']])
        execute('owned', p['pending'], grant)
        summary['owned'] = {'sidecar': ready('owned', p['service_id'])}
        for mode in ['shared', 'owned']:
            s = summary[mode]['sidecar']
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open('http://127.0.0.1:' + str(s['spec']['publish'][0]['host_port']) + '/', timeout=5) as r:
                assert r.status == 200
                transcript.setdefault('http', []).append({'mode': mode, 'status': r.status, 'sha256': hashlib.sha256(r.read()).hexdigest(), 'agent_cli_exited': True})
            actual = json.loads(subprocess.check_output(['docker', '--context', 'orbstack', 'inspect', s['container_id']]))[0]
            assert actual['HostConfig']['PortBindings'] == {'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': str(s['spec']['publish'][0]['host_port'])}]}
            status = subprocess.check_output(['docker', '--context', 'orbstack', 'exec', s['container_id'], 'cat', '/proc/1/status']).decode()
            assert 'NoNewPrivs:\t1' in status
            service(mode, 'logs', {'service_id': s['service_id'], 'max_bytes': 16384})
    elif args.phase == 'workspace':
        for mode in ['shared', 'owned']:
            q = {'request_id': uuid.uuid4().hex, 'spec': spec(mode, 'workspace')}
            s = service(mode, 'start', q)['service']
            summary[mode]['workspace'] = ready(mode, s['service_id'])
            repeat = service(mode, 'start', q)['service']
            assert repeat['service_id'] == s['service_id'] and repeat['operation_id'] == s['operation_id']
            logs = service(mode, 'logs', {'service_id': s['service_id'], 'max_bytes': 16384})
            assert b'P4_SERVICE_READY' in base64.b64decode(logs.get('data', ''))
            public = call(['--project', resource(mode)['project'], 'terminal', 'list'], {})
            assert s['terminal_id'] not in json.dumps(public)
            call(['--project', resource(mode)['project'], 'terminal', 'inspect'], {'terminal_id': s['terminal_id']}, False)
    elif args.phase == 'proxy':
        for mode in (['shared', 'owned'] if args.mode == 'both' else [args.mode]):
            grants = [expose_plan(mode) for _ in range(2)]
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                results = list(pool.map(lambda grant: execute(mode, *grant), grants))
            xs = [x for x in ports(mode) if x['state'] == 'open' and x['target']['service_id'] == summary[mode]['workspace']['service_id']]
            assert len(xs) == 2 and xs[0]['port'] != xs[1]['port']
            assert all(x['address'] == '127.0.0.1' for x in xs)
            for x in xs:
                half_close(x)
            duplicate = execute(mode, *grants[0], okay=False)
            assert duplicate['operation']['operation_id'] == results[0]['operation_id']
            with socket.socket() as occupied:
                occupied.bind(('127.0.0.1', 0))
                occupied.listen()
                p, a = expose_plan(mode, occupied.getsockname()[1])
                conflict = execute(mode, p, a, False)
                assert conflict['operation']['operation_id'] and conflict['operation']['outcome'] == 'outcome_unknown'
                with socket.create_connection(occupied.getsockname(), timeout=2):
                    pass
            close_exposure(mode, xs[0])
            half_close(xs[1])
            with socket.create_connection((xs[1]['address'], xs[1]['port']), timeout=5) as live:
                live.settimeout(12)
                live.sendall(b'LIVE')
                time.sleep(.5)
                op_id = xs[1]['operation_id']
                grant = next(g for g, op in zip(grants, results) if op['operation_id'] == op_id)
                start = time.monotonic()
                call(['policy', 'revoke', resource(mode)['project'], grant[1]['approval_id']])
                try:
                    assert live.recv(1024) == b''
                    closure = 'eof'
                except ConnectionResetError:
                    closure = 'reset'
                transcript.setdefault('revocation', []).append({'mode': mode, 'seconds': time.monotonic() - start, 'established_stream_closed': True, 'closure': closure})
            after = ports(mode)
            assert not any(x['state'] == 'open' and x['target']['service_id'] == summary[mode]['workspace']['service_id'] for x in after)
            call(['hostd', '--stop-session', session_name(mode)])
            summary[mode]['proxy'] = xs
    elif args.phase == 'manager':
        mode = 'owned'
        binding = summary[mode]['workspace']['binding']
        statefile = Path(manifest['state']) / 'ports' / (binding['project_id'] + '.json')
        control = Path(manifest['state']) / 'ports' / (binding['record_digest'][:16] + '.sock')
        children = []

        def start_manager():
            before = json.loads(statefile.read_text())['payload']['instance'] if statefile.exists() else ''
            argv = [args.reach, 'hostd', '--session', session_name(mode)]
            child_env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'REACH_HOME': manifest['state'], 'REACH_SESSION': session_name(mode), 'REACH_MANAGED': '1', 'REACH_MANAGED_PROJECT': binding['project_id'], 'REACH_MANAGED_RECORD': binding['record_digest']}
            child = subprocess.Popen(argv, cwd=manifest['state'], env=child_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True)
            children.append(child)
            deadline = time.monotonic() + 10
            while True:
                assert child.poll() is None, child.communicate()[0].decode()
                if statefile.exists():
                    now = json.loads(statefile.read_text())['payload']['instance']
                    if now != before:
                        assert control.is_socket() and control.stat().st_mode & 0o777 == 0o600
                        transcript.setdefault('manager_instances', []).append({'pid': child.pid, 'argv': argv, 'instance': now, 'parent_retains_child_handle': True})
                        save()
                        return child, now
                assert time.monotonic() < deadline, 'manager startup deadline'
                time.sleep(.025)

        try:
            child, first = start_manager()
            p, a = expose_plan(mode)
            op = execute(mode, p, a)
            # Observe the actual retained listener without starting additional
            # service observations. Transport is verified by proxy and again
            # below on the replacement instance. This phase injects an idle
            # manager crash; in-flight directory locks deliberately fail closed.
            x = next(x for x in json.loads(statefile.read_text())['payload']['exposures'] if x['operation_id'] == op['operation_id'])
            deadline = time.monotonic() + 5
            while True:
                locks = [str(p) for sub in ['services', 'service-backends'] for p in (Path(manifest['state']) / sub).rglob('*.lock')]
                if not locks:
                    break
                assert time.monotonic() < deadline, ('no idle crash point', locks)
                time.sleep(.01)
            transcript['crash_scope'] = 'idle manager, no in-flight private resource lock observed'
            save()
            # Signal only our unreaped, directly spawned child; never discover
            # a PID from a file/name or signal an unowned process.
            assert child.poll() is None
            child.kill()
            output = child.communicate(timeout=5)[0].decode()
            transcript['crashed_child'] = {'instance': first, 'exit': child.returncode, 'output': output}
            assert next(y for y in ports(mode) if y['exposure_id'] == x['exposure_id'])['state'] == 'unavailable'
            with socket.socket() as test:
                test.settimeout(1)
                assert test.connect_ex((x['address'], x['port'])) != 0
            next_child, second = start_manager()
            assert first != second
            old = next(y for y in ports(mode) if y['exposure_id'] == x['exposure_id'])
            assert old['state'] == 'unavailable' and old['instance'] == first
            repeat = execute(mode, p, a, False)
            assert repeat['operation']['operation_id'] == op['operation_id']
            with socket.socket() as test:
                test.settimeout(1)
                assert test.connect_ex((x['address'], x['port'])) != 0
            p2, a2 = expose_plan(mode)
            op2 = execute(mode, p2, a2)
            fresh = next(y for y in ports(mode) if y['operation_id'] == op2['operation_id'])
            assert fresh['instance'] == second and fresh['exposure_id'] != x['exposure_id']
            half_close(fresh)
            close_exposure(mode, fresh)
            call(['hostd', '--stop-session', session_name(mode)])
            next_child.communicate(timeout=5)
            assert next_child.returncode == 0 and not control.exists()
            transcript['old_exposure_not_replayed'] = True
            transcript['owned_listeners_released'] = True
        finally:
            for child in children:
                if child.poll() is None:
                    child.terminate()
                    child.communicate(timeout=10)
    elif args.phase == 'stop':
        for mode in ['shared', 'owned']:
            service(mode, 'stop', {'service_id': summary[mode]['workspace']['service_id']})
            s = summary[mode]['sidecar']
            p, a = plan(mode, {'capability': 'service_change', 'target_id': s['service_id'], 'generation': s['generation'], 'params': {'operation': 'stop', 'spec_digest': s['execution_digest']}})
            execute(mode, p, a)
            assert service(mode, 'inspect', {'service_id': s['service_id']})['service']['state'] == 'stopped'
    transcript['complete'] = True
    save()
    print('PASS', args.phase, flush=True)


if __name__ == '__main__':
    main()
