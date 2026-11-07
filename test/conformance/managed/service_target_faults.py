#!/usr/bin/env python3
"""Real P4 readiness/secondary-port/endpoint faults in one owned sidecar.

No global routes, VPN, settings, workspace binding or other resources change.
Every model effect uses an exact Host approval; Docker operations are explicit
operator fault injection/retirement on this newly recorded full container ID.
"""
import argparse
import copy
import ipaddress
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.request
import uuid


def main():
    parser=argparse.ArgumentParser()
    for key in ['reach','resources','evidence']:
        parser.add_argument('--'+key,required=True)
    parser.add_argument('--resume',help='explicit diagnostic retaining the same service after a diagnosed proxy failure; never reuses an exposure intent')
    args=parser.parse_args()
    evidence=Path(args.evidence)
    assert not evidence.exists(),'inspect retained outcomes; never auto-replay'
    manifest=json.loads(Path(args.resources).read_text())
    project=manifest['resources']['shared']['project']
    env=dict(os.environ,REACH_HOME=manifest['state'])
    report={'calls':[]}

    def save():evidence.write_text(json.dumps(report,indent=2))
    def invoke(words,q=None,okay=True,docker=False):
        argv=(['docker','--context','orbstack'] if docker else [args.reach])+words
        row={'argv':argv,'request':q,'state':'dispatching'};report['calls'].append(row);save()
        p=subprocess.run(argv,input=json.dumps(q) if q is not None else None,env=env,capture_output=True,text=True,timeout=40)
        row.update(state='returned',exit=p.returncode,stdout=p.stdout,stderr=p.stderr);save()
        assert (p.returncode==0)==okay,row
        return p.stdout.strip() if docker else json.loads(p.stdout)
    def service(op,q,okay=True):return invoke(['--project',project,'service',op],q,okay)
    def ports():return invoke(['--project',project,'port','list'],{})
    def approve(h):
        p=invoke(['host','plan',project],h)
        a=invoke(['policy','approve',project,p['request_id'],p['request_digest']])
        return p,a
    def execute(p,a,okay=True):return invoke(['host','execute',project],{'request_id':p['request_id'],'approval_id':a['approval_id']},okay)

    spec=copy.deepcopy(json.loads((evidence.parent/'harness-spec-shared-claude.json').read_text()))
    program="import time\nfrom http.server import HTTPServer,BaseHTTPRequestHandler\nclass H(BaseHTTPRequestHandler):\n def do_GET(self):\n  self.send_response(200);self.send_header('Content-Length','3');self.end_headers();self.wfile.write(b'P4\\n')\nprint('P4_DELAYED_READY',flush=True);time.sleep(12);HTTPServer(('0.0.0.0',8080),H).serve_forever()"
    spec.update(argv=['/usr/bin/python3','-u','-c',program],guest_ports=[8080,8081],publish=[])
    spec['health']['timeout_ms']=200
    if args.resume:
        diagnostic=json.loads(Path(args.resume).read_text())
        record=diagnostic['retained_service']
        spec=record['spec']
        report['resume_diagnostic']=args.resume
        report['original_readiness_and_secondary_port_evidence']='native-target-faults.json'
    else:
        record=service('plan',{'request_id':uuid.uuid4().hex,'spec':spec})['service']
        pending=record['pending'];grant=invoke(['policy','approve',project,pending['request_id'],pending['request_digest']])
        execute(pending,grant)
        initial=service('inspect',{'service_id':record['service_id']})['service']
        assert initial['state']=='unhealthy' and not initial['ready'],initial
        report['running_is_not_ready']=True;save()
    deadline=time.monotonic()+20
    while True:
        ready=service('inspect',{'service_id':record['service_id']})['service']
        if ready['ready']:break
        assert time.monotonic()<deadline,ready
    if args.resume:
        assert all(ready[k]==record[k] for k in ['service_id','container_id','started_at','generation','spec_digest','execution_digest']), 'retained target changed'
    cid=ready['container_id'];report['container_id']=cid;save()
    def expose(port):return approve({'capability':'port_expose','target_id':ready['service_id'],'generation':ready['generation'],'params':{'service_id':ready['service_id'],'guest_port':port,'listen':'127.0.0.1'}})
    if not args.resume:
        before=ports()
        denied,denied_grant=expose(8081)
        refusal=execute(denied,denied_grant,False)['operation']
        assert refusal['outcome']=='outcome_unknown' and refusal['operation_id']
        assert len(ports())==len(before),'closed secondary port created a listener'
        duplicate=execute(denied,denied_grant,False)['operation']
        assert duplicate['operation_id']==refusal['operation_id']
        report['secondary_port_no_listener']={'operation_id':refusal['operation_id'],'no_replay':True};save()
    p,a=expose(8080);op=execute(p,a)
    exposure=next(x for x in ports() if x['operation_id']==op['operation_id'])
    assert exposure['state']=='open'
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open('http://127.0.0.1:'+str(exposure['port'])+'/',timeout=12) as response:assert response.read()==b'P4\n'
    report['single_thread_http_proxy']=True;save()
    stream=socket.create_connection(('127.0.0.1',exposure['port']),timeout=12)
    stream.sendall(b'GET / HTTP/1.1\r\n') # established stream, deliberately incomplete headers
    time.sleep(2)
    n=json.loads(invoke(['network','inspect',spec['network_id']],docker=True))[0]
    subnet=ipaddress.ip_network(n['IPAM']['Config'][0]['Subnet'])
    used={str(ipaddress.ip_interface(x['IPv4Address']).ip) for x in n['Containers'].values()}
    gateway=n['IPAM']['Config'][0].get('Gateway')
    address=next(str(ip) for ip in subnet.hosts() if str(ip) not in used and str(ip)!=gateway and int(ip)-int(subnet.network_address)>=10)
    before_runtime=json.loads(invoke(['inspect',cid],docker=True))[0]
    assert before_runtime['Config']['Labels']['dev.agentreach.service']==ready['service_id']
    invoke(['network','disconnect',spec['network_id'],cid],docker=True)
    invoke(['network','connect','--ip',address,spec['network_id'],cid],docker=True)
    changed=json.loads(invoke(['inspect',cid],docker=True))[0]
    endpoint=next(iter(changed['NetworkSettings']['Networks'].values()))
    assert changed['Id']==cid and changed['State']['StartedAt']==ready['started_at']
    assert endpoint['IPAddress']==address and endpoint['EndpointID']!=ready['endpoint']['EndpointID']
    lost=service('inspect',{'service_id':ready['service_id']},False)['service']['service']
    assert lost['state']=='lost' and not lost['ready']
    assert lost['endpoint']==ready['endpoint'],'foreign endpoint was adopted'
    again=service('inspect',{'service_id':ready['service_id']},False)['service']['service'];assert again['state']=='lost'
    stream.settimeout(12)
    try:assert stream.recv(1024)==b''
    except ConnectionResetError:pass
    finally:stream.close()
    deadline=time.monotonic()+12
    while True:
        retained=next(x for x in ports() if x['exposure_id']==exposure['exposure_id'])
        if retained['state']=='unavailable':break
        assert time.monotonic()<deadline,retained
    with socket.socket() as rebound:
        # Match Go's listener reuse semantics: recently closed connections can
        # retain TIME_WAIT even after the listening socket has been released.
        rebound.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
        rebound.bind(('127.0.0.1',exposure['port']))
    report['endpoint_fault']={'old':ready['endpoint'],'actual_new':{'IPAddress':address,'EndpointID':endpoint['EndpointID']},'old_exposure':retained,'established_stream_closed':True,'listener_released':True,'service_remains_lost':True};save()
    invoke(['hostd','--stop-session','project-'+project])
    # Explicit operator cleanup, since a permanently lost service is not
    # authorised to mutate an externally changed endpoint through its old plan.
    logs=invoke(['logs','--tail','100',cid],docker=True);report['retired_log']=logs
    actual=json.loads(invoke(['inspect',cid],docker=True))[0]
    assert actual['Id']==cid and actual['Config']['Labels']['dev.agentreach.service']==ready['service_id']
    invoke(['stop',cid],docker=True);invoke(['rm',cid],docker=True)
    report.update(complete=True,exact_fault_container_retired=True);save()
    print('PASS real readiness, secondary port and endpoint/IP replacement faults')


if __name__=='__main__':main()
