#!/usr/bin/env python3
"""Destructive P3 T03 on explicitly task-labelled isolated fixtures only.

Run after all harness tests. Retains Guest terminal logs before restart/replacement.
The separate opt-in flag makes this unsuitable for accidental general integration use.
"""
import argparse
import base64
import time
import hashlib
import json
import os
from pathlib import Path
import subprocess
import uuid


def run():
    parser = argparse.ArgumentParser()
    parser.add_argument('--reach', required=True)
    parser.add_argument('--resources', required=True)
    parser.add_argument('--terminals', required=True)
    parser.add_argument('--allow-task-runtime-replacement', action='store_true')
    args = parser.parse_args()
    assert args.allow_task_runtime_replacement, 'explicit fixture replacement opt-in required'
    manifest = Path(args.resources)
    res = json.loads(manifest.read_text())
    terminals = json.loads(Path(args.terminals).read_text())
    evidence = Path(res['root']) / 'evidence'
    env = dict(os.environ, REACH_HOME=res['state'])
    docker = ['docker', '--context', 'orbstack']
    results = []

    def command(*words):
        return subprocess.check_output(docker + list(words), text=True).strip()

    def inspect(cid):
        actual = json.loads(command('inspect', cid))[0]
        assert actual['Id'] == cid and len(cid) == 64
        assert actual['Config']['Labels']['agentreach.task'] == res['task']
        assert actual['Config']['Labels']['dev.agentreach.managed'] == 'true'
        return actual

    def terminal(mode, op):
        q = {'terminal_id': terminals[mode]['terminal_id']}
        if op == 'write':
            q.update(input_id=uuid.uuid4().hex, key='Enter')
        p = subprocess.run([args.reach, '--project', res['resources'][mode]['project'], 'terminal', op], env=env, input=json.dumps(q), capture_output=True, text=True, timeout=40)
        result = {'mode': mode, 'op': op, 'exit': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr}
        results.append(result)
        assert p.returncode != 0 and 'lost' in p.stdout, result
        assert json.loads(p.stdout)['terminal']['state'] == 'lost', result
        return result

    def operations(mode):
        p = Path(res['state']) / 'security' / (res['resources'][mode]['project'] + '.json')
        return len(json.loads(p.read_text())['ledger']['operations'])

    def descendants(mode, cid):
        project = res['resources'][mode]['project']
        def invoke(op, q):
            p = subprocess.run([args.reach, '--project', project, 'terminal', op], env=env, input=json.dumps(q), capture_output=True, text=True, timeout=40)
            assert p.returncode == 0, (p.stdout, p.stderr)
            return json.loads(p.stdout)
        def proc(pid):
            p = subprocess.run(docker + ['exec', cid, 'cat', '/proc/' + str(pid) + '/stat'], capture_output=True, text=True)
            if p.returncode:
                return None
            fields = p.stdout[p.stdout.rfind(')') + 1:].split()
            return {'pid': pid, 'state': fields[0], 'start': fields[19]}
        canary = '/tmp/agentreach-p3-argv-' + uuid.uuid4().hex
        unusual = ['', '-n', '--', "quote'\"$()`;[]", 'line\n中', '$(touch ' + canary + ')']
        program = "import json,sys;print(json.dumps(sys.argv[1:],ensure_ascii=False))"
        argv_id = invoke('create', {'request_id': uuid.uuid4().hex, 'argv': ['/usr/bin/python3', '-c', program, *unusual], 'cols': 80, 'rows': 24})['terminal_id']
        expected = (json.dumps(unusual, ensure_ascii=False) + '\r\n').encode()
        for _ in range(20):
            r = invoke('read', {'terminal_id': argv_id, 'max_bytes': 1024})
            actual = base64.b64decode(r.get('data', ''))
            if r['complete']:
                break
            time.sleep(.02)
        assert r['complete'] and actual == expected, (r, actual, expected)
        subprocess.run(docker + ['exec', cid, '/usr/bin/python3', '-c', 'import os;assert not os.path.exists(' + repr(canary) + ')'], check=True)
        results.append({'mode': mode, 'case': 'argv-array', 'terminal_id': argv_id, 'empty_special_newline_options_equal': True, 'sha256': hashlib.sha256(actual).hexdigest()})
        program = "import subprocess,os,json,time;a=subprocess.Popen(['/usr/bin/sleep','90']);b=subprocess.Popen(['/usr/bin/sleep','90'],start_new_session=True,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);os.write(1,json.dumps({'attached':a.pid,'detached':b.pid}).encode());time.sleep(90)"
        tid = invoke('create', {'request_id': uuid.uuid4().hex, 'argv': ['/usr/bin/python3', '-c', program], 'cols': 80, 'rows': 24})['terminal_id']
        identities = {}
        try:
            for _ in range(20):
                r = invoke('read', {'terminal_id': tid, 'max_bytes': 1024})
                data = base64.b64decode(r.get('data', ''))
                try:
                    children = json.loads(data)
                    break
                except ValueError:
                    time.sleep(.02)
            else:
                raise AssertionError('child identity output missing')
            identities = {name: proc(pid) for name, pid in children.items()}
            assert all(identities.values()), identities
            closed = invoke('close', {'terminal_id': tid})
            assert closed['state'] == 'closed' and closed['descendants'] == 'unconfirmed', closed
            after = {name: proc(value['pid']) for name, value in identities.items()}
            assert after['detached'] and after['detached']['start'] == identities['detached']['start'] and after['detached']['state'] != 'Z', after
            results.append({'mode': mode, 'case': 'close-descendants', 'terminal_id': tid, 'before': identities, 'after': after, 'reported': closed['descendants']})
        finally:
            for identity in identities.values():
                actual = proc(identity['pid'])
                if actual and actual['start'] == identity['start'] and actual['state'] != 'Z':
                    subprocess.run(docker + ['exec', cid, 'kill', '-TERM', str(identity['pid'])], check=True)

    for mode in ['shared', 'owned']:
        item = res['resources'][mode]
        cid = item['id']
        before = inspect(cid)
        assert before['State']['Running']
        descendants(mode, cid)
        name = before['Name'].removeprefix('/')
        archive = evidence / ('pre-restart-' + mode + '-terminals.tar.gz')
        assert not archive.exists(), 'inspect prior T03 state before retry'
        with archive.open('wb') as output:
            p = subprocess.run(docker + ['exec', cid, 'tar', '-C', '/tmp', '-czf', '-', 'agentreach-terminals-10001'], stdout=output, stderr=subprocess.PIPE, check=True)
        results.append({'mode': mode, 'archive_sha256': hashlib.sha256(archive.read_bytes()).hexdigest(), 'tar_stderr': p.stderr.decode()})
        old_count = operations(mode)
        command('stop', cid)
        stopped = inspect(cid)
        assert not stopped['State']['Running']
        command('start', cid)
        restarted = inspect(cid)
        assert restarted['State']['StartedAt'] != before['State']['StartedAt']
        terminal(mode, 'inspect')
        terminal(mode, 'write')
        assert operations(mode) == old_count, 'lost terminal dispatched an intent'
        # The same immutable ID now has a different generation; memory is gone.
        probe = "import pathlib; names=[]\nfor p in pathlib.Path('/proc').glob('[0-9]*/comm'):\n try: names.append(p.read_text().strip())\n except FileNotFoundError: pass\nassert 'tmux: server' not in names,names"
        subprocess.run(docker + ['exec', cid, '/usr/bin/python3', '-c', probe], check=True)
        item['t03_restart'] = {'before': before['State']['StartedAt'], 'after': restarted['State']['StartedAt'], 'old_terminal_id': terminals[mode]['terminal_id']}
        inspect(cid)  # repeat exact task ownership check immediately before deletion
        command('rm', '-f', cid)
        replacement = command('run', '-d', '--name', name, '--label', 'agentreach.task=' + res['task'], '--label', 'dev.agentreach.managed=true', '--label', 'dev.agentreach.project=' + item['project'], '--user', '10001:10001', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--network=none', '--workdir', '/workspace/p3', '--mount', item['mount'], before['Image'], 'sleep', 'infinity')
        assert replacement != cid
        inspect(replacement)
        item['replacement_id'] = replacement
        item['original_retired'] = True
        manifest.write_text(json.dumps(res, indent=2))
        terminal(mode, 'inspect')
        terminal(mode, 'write')
        assert operations(mode) == old_count, 'same-name replacement inherited old terminal'
        results.append({'mode': mode, 'original_id': cid, 'replacement_id': replacement, 'same_name': name, 'memory_restored': False})
        (evidence / 'terminal-lifecycle.json').write_text(json.dumps(results, indent=2))
        print(mode, 'restart and same-name replacement lost checks passed', flush=True)


if __name__ == '__main__':
    run()
