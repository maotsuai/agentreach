#!/usr/bin/env python3
"""P3 native CLI acceptance against explicitly owned isolated fixtures; no model calls.

The resources manifest must name newly provisioned task-labelled containers.
Runtime restart tests are separate, opt-in, and run after harness persistence tests.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import subprocess
import time
import threading
import uuid


def run():
    parser = argparse.ArgumentParser()
    parser.add_argument('--reach', required=True)
    parser.add_argument('--resources', required=True)
    parser.add_argument('--evidence', required=True)
    parser.add_argument('--resume', help='retained REPL summary from an earlier partial run')
    args = parser.parse_args()
    resources = json.loads(Path(args.resources).read_text())
    evidence = Path(args.evidence)
    evidence.mkdir(parents=True, exist_ok=True)
    (evidence / 'candidate.json').write_text(json.dumps({'reach_sha256': hashlib.sha256(Path(args.reach).read_bytes()).hexdigest(), 'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), 'resources': resources}, indent=2))
    env = dict(os.environ, REACH_HOME=resources['state'])
    docker = ['docker', '--context', 'orbstack']
    transcript = []
    summary = json.loads(Path(args.resume).read_text()) if args.resume else {}
    transcript_lock = threading.Lock()

    def call(mode, op, q, okay=True):
        p = subprocess.run([args.reach, '--project', resources['resources'][mode]['project'], 'terminal', op], input=json.dumps(q), env=env, text=True, capture_output=True, timeout=40)
        with transcript_lock:
            transcript.append({'mode': mode, 'op': op, 'request': q, 'exit': p.returncode, 'stdout': p.stdout, 'stderr': p.stderr})
            (evidence / 'transcript.json').write_text(json.dumps(transcript, indent=2))
        if okay:
            assert p.returncode == 0, (op, p.stdout, p.stderr)
        else:
            assert p.returncode != 0, (op, p.stdout)
        return json.loads(p.stdout) if p.stdout else {'error': p.stderr}

    def create(mode, argv):
        q = {'request_id': uuid.uuid4().hex, 'argv': argv, 'cols': 80, 'rows': 24}
        r = call(mode, 'create', q)
        repeat = call(mode, 'create', q)
        assert repeat['terminal_id'] == r['terminal_id']
        return r['terminal_id']

    def write(mode, tid, data=None, key=None, input_id=None):
        q = {'terminal_id': tid, 'input_id': input_id or uuid.uuid4().hex}
        if data is not None:
            q['data'] = base64.b64encode(data).decode()
        if key:
            q['key'] = key
        return call(mode, 'write', q), q

    def until(mode, tid, expected=None, cursor='', complete=False):
        data = b''
        for _ in range(80):
            r = call(mode, 'read', {'terminal_id': tid, 'cursor': cursor, 'max_bytes': 16384})
            data += base64.b64decode(r.get('data', ''))
            cursor = r['next_cursor']
            if (expected is None or expected in data) and (not complete or r['complete']):
                return data, cursor, r
            assert not r.get('gap'), r
            time.sleep(.02)
        raise AssertionError(('output deadline', tid, expected, data[-500:], r))

    for mode in ['shared', 'owned']:
        cid = resources['resources'][mode]['id']
        actual = json.loads(subprocess.check_output(docker + ['inspect', cid]))[0]
        assert actual['Config']['Labels']['agentreach.task'] == resources['task']
        if mode in summary:
            tid, cursor = summary[mode]['terminal_id'], summary[mode]['cursor']
            assert call(mode, 'attach', {'terminal_id': tid})['state'] == 'running'
        else:
            tid = create(mode, ['/usr/bin/python3', '-q'])
            cursor = ''
            for n in range(11):
                command = 'p3_value=0' if n == 0 else 'p3_value+=1'
                _, q = write(mode, tid, (command + "; print('P3_ROUND_" + str(n) + "',p3_value)").encode())
                first = call(mode, 'write', q)
                assert first['accepted']
                write(mode, tid, key='Enter')
                data, cursor, _ = until(mode, tid, ('P3_ROUND_' + str(n) + ' ' + str(n)).encode(), cursor)
                assert b'Traceback' not in data
            call(mode, 'resize', {'terminal_id': tid, 'cols': 113, 'rows': 41})
            write(mode, tid, b"import os; print('P3_SIZE',os.get_terminal_size(0))")
            write(mode, tid, key='Enter')
            _, cursor, _ = until(mode, tid, b'columns=113, lines=41', cursor)
            write(mode, tid, b'exec("while True: pass")')
            write(mode, tid, key='Enter')
            call(mode, 'interrupt', {'terminal_id': tid, 'signal': 'Ctrl-C'})
            _, cursor, _ = until(mode, tid, b'KeyboardInterrupt', cursor)
            write(mode, tid, b"print('P3_AFTER_INTERRUPT',p3_value)")
            write(mode, tid, key='Enter')
            _, cursor, _ = until(mode, tid, b'P3_AFTER_INTERRUPT 10', cursor)
            call(mode, 'interrupt', {'terminal_id': tid, 'signal': 'SIGTERM'}, okay=False)
            # Distinct requests race through the same cross-process project lock.
            def concurrent_write(n):
                return write(mode, tid, ('print("P3_CONCURRENT_' + str(n) + '")\r').encode())[0]
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                assert all(x['accepted'] for x in pool.map(concurrent_write, range(2)))
            summary[mode] = {'terminal_id': tid, 'cursor': cursor, 'value': 10, 'project': resources['resources'][mode]['project']}
        # Fast exit, long/no-newline, ANSI, all byte values and UTF-8: compare actual complete PTY bytes.
        payload = b'FIRST\x1b[31m\xe4\xb8\xad' + bytes(range(256)) * 1024 + b'LAST'
        program = "import os,tty;tty.setraw(0);os.write(1,b'FIRST\\x1b[31m\\xe4\\xb8\\xad'+bytes(range(256))*1024+b'LAST')"
        fast = create(mode, ['/usr/bin/python3', '-c', program])
        data, final_cursor, final = until(mode, fast, b'LAST', complete=True)
        assert data == payload, (len(data), len(payload), hashlib.sha256(data).hexdigest())
        repeat = call(mode, 'read', {'terminal_id': fast, 'max_bytes': 16384})
        again = call(mode, 'read', {'terminal_id': fast, 'max_bytes': 16384})
        assert repeat['data'] == again['data'] and repeat['next_cursor'] == again['next_cursor']
        call(mode, 'read', {'terminal_id': fast, 'cursor': fast + ':' + uuid.uuid4().hex + ':0', 'max_bytes': 1}, okay=False)
        split = create(mode, ['/usr/bin/python3', '-c', "import os;os.write(1,'中'.encode())"])
        until(mode, split, '中'.encode(), complete=True)
        a = call(mode, 'read', {'terminal_id': split, 'max_bytes': 1})
        b = call(mode, 'read', {'terminal_id': split, 'max_bytes': 1, 'cursor': a['next_cursor']})
        c = call(mode, 'read', {'terminal_id': split, 'max_bytes': 1, 'cursor': b['next_cursor']})
        assert b''.join(base64.b64decode(x['data']) for x in [a, b, c]) == '中'.encode()
        # Raw input proof is independent of REPL line discipline.
        raw = b'A\x00B\x03C\rD\n\x1b[200~paste\x1b[201~\xff'
        program = "import os,tty;tty.setraw(0);os.write(1,b'READY');b=b''\nwhile len(b)<" + str(len(raw)) + ": b+=os.read(0,4096)\nos.write(1,b.hex().encode())"
        raw_id = create(mode, ['/usr/bin/python3', '-c', program])
        _, raw_cursor, _ = until(mode, raw_id, b'READY')
        write(mode, raw_id, raw)
        data, _, _ = until(mode, raw_id, raw.hex().encode(), raw_cursor, complete=True)
        assert data == raw.hex().encode(), data
        # Output exhaustion retains a bounded prefix and reports the gap explicitly.
        limited = create(mode, ['/usr/bin/python3', '-c', "import os,tty;tty.setraw(0);os.write(1,b'L'*(9<<20))"])
        limit_deadline = time.monotonic() + 90
        while time.monotonic() < limit_deadline:
            limit_result = call(mode, 'inspect', {'terminal_id': limited})
            if limit_result.get('gap') == 'output_limit':
                break
            time.sleep(.05)
        assert limit_result.get('gap') == 'output_limit' and not limit_result['complete'], limit_result
        log = '/tmp/agentreach-terminals-10001/' + limited + '/output'
        size = int(subprocess.check_output(docker + ['exec', cid, 'stat', '-c', '%s', log]))
        assert size == 8 << 20, size
        prefix = call(mode, 'read', {'terminal_id': limited, 'max_bytes': 16384})
        assert prefix['gap'] == 'output_limit' and base64.b64decode(prefix['data']) == b'L' * 16384
        # Collector death cannot silently stop collection while claiming a healthy stream.
        broken = create(mode, ['/usr/bin/python3', '-q'])
        path = '/tmp/agentreach-terminals-10001/' + broken
        pr = json.loads(subprocess.check_output(docker + ['exec', cid, 'cat', path + '/progress.json']))
        pid = pr['collector'].split(':')[1]
        cmdline = subprocess.check_output(docker + ['exec', cid, 'cat', '/proc/' + pid + '/cmdline'])
        assert b'terminal-collect' in cmdline and broken.encode() in cmdline
        subprocess.run(docker + ['exec', cid, 'kill', '-KILL', pid], check=True)
        fault = call(mode, 'inspect', {'terminal_id': broken})
        assert fault.get('gap') == 'collector_failed' and not fault['complete'], fault
        call(mode, 'write', {'terminal_id': broken, 'input_id': uuid.uuid4().hex, 'key': 'Enter'}, okay=False)
        assert call(mode, 'close', {'terminal_id': broken})['state'] == 'closed'
        # The unaffected original REPL still holds its variable after exact close.
        write(mode, tid, b"print('P3_ISOLATED',p3_value)")
        write(mode, tid, key='Enter')
        _, cursor, _ = until(mode, tid, ('P3_ISOLATED '+str(summary[mode]['value'])).encode(), cursor)
        summary[mode]['cursor'] = cursor
        # Log truncation/deletion are explicit, and never manufacture an offset-zero stream.
        truncated = create(mode, ['/usr/bin/python3', '-c', "print('retained')"])
        until(mode, truncated, b'retained', complete=True)
        log = '/tmp/agentreach-terminals-10001/' + truncated + '/output'
        subprocess.run(docker + ['exec', cid, 'truncate', '-s', '0', log], check=True)
        assert call(mode, 'read', {'terminal_id': truncated, 'max_bytes': 10}).get('gap') == 'log_changed'
        deleted = create(mode, ['/usr/bin/python3', '-c', "print('retained')"])
        until(mode, deleted, b'retained', complete=True)
        subprocess.run(docker + ['exec', cid, 'rm', '/tmp/agentreach-terminals-10001/' + deleted + '/output'], check=True)
        assert call(mode, 'inspect', {'terminal_id': deleted}).get('gap') == 'log_missing'
        print(mode, 'native terminal matrix passed', flush=True)
        (evidence / 'summary.json').write_text(json.dumps(summary, indent=2))


if __name__ == '__main__':
    run()
