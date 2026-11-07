#!/usr/bin/env python3
"""Bounded P4 real-CLI denials and generated-file tamper, no mutation replay."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser()
    for name in ['reach', 'resources', 'evidence']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    manifest = json.loads(Path(args.resources).read_text())
    env = dict(os.environ, REACH_HOME=manifest['state'])
    evidence = Path(args.evidence)
    assert not evidence.exists(), 'inspect original outcomes before any further action'
    report = {'reach_sha256': hashlib.sha256(Path(args.reach).read_bytes()).hexdigest(), 'calls': []}

    def save():
        evidence.write_text(json.dumps(report, indent=2))

    def call(words, q=None, okay=False):
        record = {'argv': words, 'input': q, 'state': 'dispatching'}
        report['calls'].append(record); save()
        p = subprocess.run([args.reach] + words, input=json.dumps(q) if q is not None else None, capture_output=True, text=True, env=env, timeout=35)
        record.update(state='returned', exit=p.returncode, stdout=p.stdout, stderr=p.stderr); save()
        assert (p.returncode == 0) == okay, record
        return json.loads(p.stdout) if p.stdout else None

    def containers():
        return subprocess.check_output(['docker', '--context', 'orbstack', 'ps', '-aq', '--no-trunc'], text=True).splitlines()

    mode = 'shared'
    project = manifest['resources'][mode]['project']
    root = Path(manifest['state'])
    statefile = root / 'services' / project / 'state.json'
    original_state = hashlib.sha256(statefile.read_bytes()).hexdigest()
    original_containers = containers()
    specfile = evidence.parent / 'harness-spec-shared-claude.json'
    base = json.loads(specfile.read_text())
    cases = [('root-user', 'user', '0:0'), ('host-network', 'network_id', 'host'), ('mutable-image', 'image', 'python:latest'), ('unknown-image', 'image', 'sha256:' + 'f' * 64), ('host-secret', 'env', {'API_TOKEN': 'dummy-denied'}), ('arbitrary-compose', 'build', '/tmp/denied'), ('wildcard-port', 'publish', [{'guest_port':8080,'host_port':48214,'listen':'0.0.0.0'}]), ('ipv6-port', 'publish', [{'guest_port':8080,'host_port':48214,'listen':'::'}]), ('unapproved-port', 'health', {'protocol':'tcp','port':8081,'timeout_ms':1000}), ('relative-cwd', 'cwd', '../outside'), ('host-control-mount', 'mounts', [{'type':'bind','source':manifest['state'],'target':'/control','read_only':False}]), ('docker-socket', 'mounts', [{'type':'bind','source':'/var/run/docker.sock','target':'/var/run/docker.sock','read_only':False}])]
    for name, field, value in cases:
        spec = copy.deepcopy(base); spec[field] = value
        report['case'] = name
        call(['--project', project, 'service', 'plan'], {'request_id':uuid.uuid4().hex,'spec':spec})
    owned = json.loads((evidence.parent / 'native-summary.json').read_text())['owned']['sidecar']
    call(['--project',project,'service','inspect'],{'service_id':owned['service_id']})
    call(['--project',project,'port','inspect'],{'exposure_id':owned['fixed_exposure_ids'][0]})
    call(['host','plan',project],{'capability':'open_url','target_id':'denied','generation':1,'params':{'path':'https://example.invalid/'}})
    assert original_state == hashlib.sha256(statefile.read_bytes()).hexdigest(), 'denied request altered service state'
    assert original_containers == containers(), 'denied request changed containers'
    report['denials_zero_service_or_container_change'] = True

    # An operator-owned fault in this task's exact generated file must fail
    # before durable intent and Docker mutation. Always restore original bytes.
    spec = copy.deepcopy(base)
    spec['publish'][0]['host_port'] = 48214
    record = call(['--project',project,'service','plan'],{'request_id':uuid.uuid4().hex,'spec':spec},True)['service']
    pending = record['pending']
    grant = call(['policy','approve',project,pending['request_id'],pending['request_digest']],okay=True)
    file = root / 'services' / project / record['service_id'] / 'compose.json'
    original = file.read_bytes()
    before = call(['policy','inspect',project],okay=True)
    try:
        file.write_text('{"services":{"app":{"privileged":true}}}')
        call(['host','execute',project],{'request_id':pending['request_id'],'approval_id':grant['approval_id']})
    finally:
        file.write_bytes(original)
    after = call(['policy','inspect',project],okay=True)
    assert before['operations'] == after['operations'], 'tampered file consumed intent'
    assert original_containers == containers(), 'tampered file changed containers'
    assert file.read_bytes() == original
    report['tamper_rejected_before_intent'] = {'service_id':record['service_id'],'request_id':pending['request_id'],'original_sha256':hashlib.sha256(original).hexdigest(),'restored':True}
    report['complete'] = True; save(); print('PASS real P4 denials and render tamper')


if __name__ == '__main__':
    main()
