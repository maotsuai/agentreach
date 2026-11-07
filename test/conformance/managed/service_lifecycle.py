#!/usr/bin/env python3
"""P4 exact stop and owned-fixture runtime retirement, after harness acceptance.

Explicitly destructive to the task's owned container only. Never rebind the
project, adopt the replacement, remove volumes or touch dev/old-P0 runtimes.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time
import uuid


def main():
    parser = argparse.ArgumentParser()
    for key in ['reach', 'resources', 'evidence']:
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--allow-owned-runtime-retirement', action='store_true')
    args = parser.parse_args()
    assert args.allow_owned_runtime_retirement, 'explicit task-owned retirement required'
    resource_file = Path(args.resources)
    manifest = json.loads(resource_file.read_text())
    evidence = Path(args.evidence)
    assert not evidence.exists(), 'inspect unknown outcomes; never rerun retirement'
    summary = json.loads((evidence.parent / 'native-summary.json').read_text())
    p3 = json.loads((evidence.parent / 'p3-regression-native.json').read_text())['terminals']
    env = dict(os.environ, REACH_HOME=manifest['state'])
    report = {'reach_sha256':hashlib.sha256(Path(args.reach).read_bytes()).hexdigest(),'calls':[]}

    def save():
        evidence.write_text(json.dumps(report, indent=2))

    def invoke(argv, q=None, okay=True, reach=True):
        words = ([args.reach] if reach else ['docker','--context','orbstack']) + argv
        row = {'argv':words,'input':q,'state':'dispatching'}
        report['calls'].append(row);save()
        p = subprocess.run(words,input=json.dumps(q) if q is not None else None,env=env,capture_output=True,text=True,timeout=45)
        row.update(state='returned',exit=p.returncode,stdout=p.stdout,stderr=p.stderr);save()
        assert (p.returncode==0)==okay,row
        return json.loads(p.stdout) if p.stdout and reach else p.stdout.strip()

    def service(mode,op,q,okay=True):
        return invoke(['--project',manifest['resources'][mode]['project'],'service',op],q,okay)

    def actual(cid):
        r=json.loads(invoke(['inspect',cid],reach=False))[0]
        assert r['Id']==cid and len(cid)==64 and r['Config']['Labels']['agentreach.task']==manifest['task']
        return r

    # First and repeated workspace stop must retain the exact receipt and the
    # limitation on detached descendants. Preserve shared WebUI for pending N02.
    for mode in ['shared','owned']:
        s=summary[mode]['workspace']
        service(mode,'logs',{'service_id':s['service_id'],'max_bytes':16384})
        first=service(mode,'stop',{'service_id':s['service_id']})['service']
        repeated=service(mode,'stop',{'service_id':s['service_id']})['service']
        observed=service(mode,'inspect',{'service_id':s['service_id']})['service']
        assert first['state']==repeated['state']==observed['state']=='stopped'
        assert first['operation_id']==repeated['operation_id'] and first['descendants']==repeated['descendants']==observed['descendants']=='unconfirmed'
    mode='owned';item=manifest['resources'][mode];cid=item['id']
    assert cid not in ['622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c','670140ac13d8ad3fa9b7e1696f67a0328faae646e724fea74071b3898084b799']
    before=actual(cid)
    sidecar=summary[mode]['sidecar'];project=item['project']
    service(mode,'logs',{'service_id':sidecar['service_id'],'max_bytes':16384})
    pending=invoke(['host','plan',project],{'capability':'service_change','target_id':sidecar['service_id'],'generation':sidecar['generation'],'params':{'operation':'stop','spec_digest':sidecar['execution_digest']}})
    grant=invoke(['policy','approve',project,pending['request_id'],pending['request_digest']])
    invoke(['host','execute',project],{'request_id':pending['request_id'],'approval_id':grant['approval_id']})
    assert service(mode,'inspect',{'service_id':sidecar['service_id']})['service']['state']=='stopped'
    spec=copy.deepcopy(summary[mode]['workspace']['spec'])
    spec['argv']=[x.replace('8091','8092') for x in spec['argv']]
    spec['health']['port']=8092;spec['guest_ports']=[8092]
    q={'request_id':uuid.uuid4().hex,'spec':spec}
    started=service(mode,'start',q)['service']
    deadline=time.monotonic()+15
    while True:
        running=service(mode,'inspect',{'service_id':started['service_id']})['service']
        if running['ready']:break
        assert time.monotonic()<deadline,running
    service(mode,'logs',{'service_id':running['service_id'],'max_bytes':16384})
    operations=invoke(['policy','inspect',project])['operations']
    archive=evidence.parent/'owned-workspace-before-retirement.tar'
    assert not archive.exists()
    with archive.open('wb') as output:
        subprocess.run(['docker','--context','orbstack','cp',cid+':/workspace/project/.','-'],stdout=output,check=True)
    report['workspace_archive']={'path':str(archive),'sha256':hashlib.sha256(archive.read_bytes()).hexdigest()};save()

    invoke(['restart',cid],reach=False)
    restarted=actual(cid)
    assert restarted['State']['Running'] and restarted['State']['StartedAt']!=before['State']['StartedAt']

    def assert_lost():
        lost=service(mode,'inspect',{'service_id':running['service_id']},False)['service']['service']
        assert lost['state']=='lost' and not lost['ready'] and lost['operation_id']==started['operation_id'],lost
        duplicate=service(mode,'start',q,False)['service']['service']
        assert duplicate['state']=='lost' and duplicate['operation_id']==started['operation_id'],duplicate
        terminal=invoke(['--project',project,'terminal','inspect'],{'terminal_id':p3[mode]['terminal_id']},False)
        assert terminal['terminal']['state']=='lost',terminal
        assert invoke(['policy','inspect',project])['operations']==operations,'retired binding dispatched an effect'
        return lost

    report['restart_lost']=assert_lost();save()
    # Full-ID checks bracket retirement; no volume deletion or re-registration.
    assert actual(cid)['State']['StartedAt']==restarted['State']['StartedAt']
    invoke(['stop',cid],reach=False);invoke(['rm',cid],reach=False)
    replacement=invoke(['create','--name',before['Name'].lstrip('/'),'--label','agentreach.task='+manifest['task'],'--label','agentreach.test=identity-replacement','--user','10001:10001','--cap-drop=ALL','--security-opt=no-new-privileges','--network=none','--memory=64m','--cpus=0.1','--pids-limit=32',before['Image'],'sleep','infinity'],reach=False)
    assert replacement!=cid and len(replacement)==64
    report['replacement_id']=replacement;save()
    invoke(['start',replacement],reach=False)
    assert actual(replacement)['State']['Running']
    report['same_name_lost']=assert_lost();save()
    assert actual(replacement)['Config']['Labels']['agentreach.test']=='identity-replacement'
    invoke(['stop',replacement],reach=False);invoke(['rm',replacement],reach=False)
    item.update(original_retired=True,replacement_id=replacement,replacement_retired=True)
    resource_file.write_text(json.dumps(manifest,indent=2))
    report.update(complete=True,original_retired=True,replacement_retired=True,owned_volume_preserved=True);save()
    print('PASS P4 stop, owned runtime restart and same-name replacement lost')


if __name__=='__main__':
    main()
