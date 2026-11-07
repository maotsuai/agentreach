#!/usr/bin/env python3
"""Local P4 network acceptance around an operator-controlled VPN transition.

This driver never toggles VPN/settings/routes or restarts the engine. Run its
three phases around the actual native VPN controls. An external LAN-device
negative test and a forced VPN-route outage remain separate acceptance evidence.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import urllib.request


def main():
    parser = argparse.ArgumentParser()
    for key in ['reach', 'resources', 'evidence', 'service-id', 'vpn-id']:
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--phase', choices=['baseline', 'vpn-up', 'vpn-down'], required=True)
    args = parser.parse_args()
    path = Path(args.evidence)
    manifest = json.loads(Path(args.resources).read_text())
    project = manifest['resources']['shared']['project']
    env = dict(os.environ, REACH_HOME=manifest['state'])
    if args.phase == 'baseline':
        assert not path.exists(), 'inspect retained outcomes; never replay a phase'
        report = {'reach_sha256': hashlib.sha256(Path(args.reach).read_bytes()).hexdigest(),
                  'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                  'service_id': args.service_id, 'project': project, 'vpn_id': args.vpn_id,
                  'phases': {}, 'calls': []}
    else:
        report = json.loads(path.read_text())
        assert report['service_id'] == args.service_id and report['project'] == project
        assert report['vpn_id'] == args.vpn_id
        assert report['script_sha256'] == hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        assert report['reach_sha256'] == hashlib.sha256(Path(args.reach).read_bytes()).hexdigest()
        assert report['phases']['baseline']['complete']
    assert args.phase not in report['phases'], 'phase already attempted; inspect instead of replay'
    phase = report['phases'][args.phase] = {'state': 'starting'}

    def save():
        path.write_text(json.dumps(report, indent=2) + '\n')

    def run(argv, q=None):
        row = {'argv': argv, 'request': q, 'state': 'dispatching'}
        report['calls'].append(row)
        save()
        p = subprocess.run(argv, input=json.dumps(q) if q is not None else None,
                           env=env, capture_output=True, text=True, timeout=40)
        row.update(state='returned', exit=p.returncode, stdout=p.stdout, stderr=p.stderr)
        save()
        assert p.returncode == 0, row
        return p.stdout

    def reach(words, q=None):
        return json.loads(run([args.reach] + words, q))

    def inspect():
        return reach(['--project', project, 'service', 'inspect'], {'service_id': args.service_id})['service']

    def approve_execute(request):
        pending = reach(['host', 'plan', project], request)
        approval = reach(['policy', 'approve', project, pending['request_id'], pending['request_digest']])
        return reach(['host', 'execute', project],
                     {'request_id': pending['request_id'], 'approval_id': approval['approval_id']})

    def http(port):
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open('http://127.0.0.1:' + str(port) + '/', timeout=20) as response:
            body = response.read(65537)
            assert response.status == 200 and len(body) <= 65536
            return {'status': response.status, 'body_sha256': hashlib.sha256(body).hexdigest()}

    save()
    # Record only the status line, never VPN configuration/credentials.
    p = subprocess.run(['scutil', '--nc', 'status', args.vpn_id], capture_output=True, text=True, timeout=10)
    phase['vpn_status'] = p.stdout.splitlines()[0] if p.stdout else ''
    phase['vpn_status_exit'] = p.returncode
    expected = 'Connected' if args.phase == 'vpn-up' else 'Disconnected'
    assert p.returncode == 0 and phase['vpn_status'] == expected, phase
    current = inspect()
    assert current['ready'] and current['state'] == 'running', current
    if args.phase == 'baseline':
        fixture = json.loads(run(['docker', '--context', 'orbstack', 'inspect', manifest['resources']['shared']['id']]))[0]
        assert fixture['Config']['Labels']['agentreach.task'] == manifest['task']
        backend = json.loads(run(['docker', '--context', 'orbstack', 'inspect', current['container_id']]))[0]
        assert backend['Config']['Labels']['dev.agentreach.service'] == args.service_id
        report['original_service'] = current
        result = approve_execute({'capability': 'port_expose', 'target_id': args.service_id,
                                  'generation': current['generation'],
                                  'params': {'service_id': args.service_id, 'guest_port': 8080,
                                             'host_port': 0, 'listen': '127.0.0.1'}})
        xs = reach(['--project', project, 'port', 'list'], {})
        report['exposure'] = next(x for x in xs if x['operation_id'] == result['operation_id'])
        save()
    original = report['original_service']
    assert all(current[key] == original[key] for key in
               ['service_id', 'generation', 'container_id', 'started_at', 'endpoint', 'execution_digest'])
    exposure = report['exposure']
    xs = reach(['--project', project, 'port', 'list'], {})
    retained = next(x for x in xs if x['exposure_id'] == exposure['exposure_id'])
    assert retained == exposure and exposure['state'] == 'open'
    phase['fixed_http'] = http(current['spec']['publish'][0]['host_port'])
    phase['dynamic_http'] = http(exposure['port'])
    assert phase['fixed_http']['body_sha256'] == phase['dynamic_http']['body_sha256']
    if args.phase != 'baseline':
        assert phase['fixed_http']['body_sha256'] == report['phases']['baseline']['fixed_http']['body_sha256']
    phase['target_route'] = run(['/sbin/route', '-n', 'get', current['endpoint']['IPAddress']])
    phase['default_route'] = run(['/sbin/route', '-n', 'get', 'default'])
    phase['configured_lan_forwarding'] = run(['orb', 'config', 'get', 'docker.expose_ports_to_lan']).strip()
    if args.phase == 'vpn-down':
        assert report['phases']['vpn-up']['complete']
        approve_execute({'capability': 'port_expose', 'target_id': exposure['exposure_id'],
                         'generation': exposure['target']['generation'],
                         'params': {'operation': 'close', 'service_id': args.service_id,
                                    'listen': '127.0.0.1'}})
        with socket.socket() as rebound:
            rebound.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            rebound.bind(('127.0.0.1', exposure['port']))
        run([args.reach, 'hostd', '--stop-session', 'project-' + project])
        phase['own_listener_released'] = True
        report['complete'] = True
        report['coverage'] = {'vpn_connect_disconnect': 'real local configured VPN',
                              'forced_route_outage': 'not injected by this driver',
                              'independent_lan_device_negative': 'not performed',
                              'engine_restarted': False}
    phase.update(state='returned', complete=True)
    save()
    print('PASS local P4 network phase', args.phase)


if __name__ == '__main__':
    main()
