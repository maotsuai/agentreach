#!/usr/bin/env python3
"""Restart a real managed Claude/Codex process and continue an existing Guest REPL.

All model requests go to a localhost scripted server with fictitious credentials.
Run twice per harness, against the same terminal_id, to prove complete restart.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import threading
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


NAMES = ['terminal_attach', 'terminal_write', 'terminal_read']


def payload(value):
    if isinstance(value, str):
        try:
            return payload(json.loads(value.split('\nOutput:\n', 1)[-1]))
        except (ValueError, TypeError):
            return None
    if isinstance(value, dict):
        if 'terminal_id' in value and 'state' in value:
            return value
        for child in value.values():
            found = payload(child)
            if found:
                return found
    if isinstance(value, list):
        for child in value:
            found = payload(child)
            if found:
                return found
    return None


def tools_in(items, namespace=None):
    found = {}
    for item in items:
        if item.get('type') == 'namespace':
            found.update(tools_in(item.get('tools', []), item.get('name')))
        else:
            for name in NAMES:
                if item.get('name', '').endswith(name):
                    found[name] = (item['name'], namespace)
    return found


def run():
    parser = argparse.ArgumentParser()
    for key in ['reach', 'binary', 'project', 'terminal-id', 'cursor', 'evidence']:
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--harness', choices=['claude', 'codex'], required=True)
    parser.add_argument('--expected', type=int, required=True)
    args = parser.parse_args()
    state = os.environ['REACH_HOME']
    evidence = Path(args.evidence)
    evidence.mkdir(parents=True, exist_ok=True)
    observed, catalog, failures = [], [], []
    step, waiting, reads = 0, False, 0
    pending_id = None
    cursor = args.cursor
    received = b''
    input_ids = [uuid.uuid4().hex, uuid.uuid4().hex]

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            nonlocal step, waiting, reads, cursor, received, pending_id
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if self.path.endswith('/count_tokens'):
                self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(b'{"input_tokens":0}'); return
            candidates, outputs = list(request.get('tools', [])), []
            if args.harness == 'codex':
                for item in request.get('input', []):
                    if item.get('type') in ['tool_search_output', 'additional_tools']:
                        candidates.extend(item.get('tools', []))
                    if item.get('type') == 'function_call_output' and item.get('call_id') == pending_id:
                        outputs.append(item.get('output', ''))
            else:
                for message in request.get('messages', []):
                    if isinstance(message.get('content'), list):
                        outputs.extend(x.get('content', '') for x in message['content'] if x.get('type') == 'tool_result' and x.get('tool_use_id') == pending_id)
            catalog.append(candidates)
            (evidence / 'catalog.json').write_text(json.dumps(catalog, indent=2))
            try:
                if waiting and outputs:
                    result = payload(outputs[-1])
                    observed.append({'step': step, 'result': outputs[-1]})
                    assert result and result['terminal_id'] == args.terminal_id, outputs[-1]
                    if step == 0:
                        assert result['state'] == 'running', result
                    elif step in [1, 2]:
                        assert result['accepted'] and result['input_id'] == input_ids[step - 1] and result['operation_id'], result
                    else:
                        reads += 1
                        assert not result.get('gap'), result
                        received += base64.b64decode(result.get('data', ''))
                        cursor = result['next_cursor']
                        if ('P3_HARNESS_VALUE ' + str(args.expected)).encode() not in received:
                            assert reads < 6, received
                            step -= 1
                    step += 1
                    waiting = False
                    pending_id = None
                found = tools_in(candidates)
                encoded = json.dumps(candidates)
                for forbidden in ['mcp__reach_host__approve', 'mcp__reach_host__revoke', 'mcp__reach_host__host_shell', 'mcp__evil__']:
                    assert forbidden not in encoded, forbidden
                name = ['terminal_attach', 'terminal_write', 'terminal_write', 'terminal_read'][step] if step < 4 else None
                arguments = {'terminal_id': args.terminal_id}
                if step == 1:
                    arguments.update(input_id=input_ids[0], data=base64.b64encode(b"p3_value+=100; print('P3_HARNESS_VALUE',p3_value)").decode())
                elif step == 2:
                    arguments.update(input_id=input_ids[1], key='Enter')
                elif step == 3:
                    arguments.update(cursor=cursor, max_bytes=16384)
                self.send_response(200); self.send_header('Content-Type', 'text/event-stream'); self.send_header('Connection', 'close'); self.end_headers()

                def event(name, value):
                    self.wfile.write(('event: ' + name + '\ndata: ' + json.dumps(value) + '\n\n').encode()); self.wfile.flush()

                if args.harness == 'codex':
                    event('response.created', {'type': 'response.created', 'response': {'id': 'p3'}})
                    if name and name in found:
                        full, namespace = found[name]
                        item = {'type': 'function_call', 'name': full, 'call_id': 'p3_' + str(step) + '_' + str(reads), 'arguments': json.dumps(arguments)}
                        if namespace:
                            item['namespace'] = namespace
                        waiting = True
                        pending_id = item['call_id']
                    elif name:
                        assert len(catalog) <= 24, 'terminal tool discovery exhausted'
                        item = {'type': 'tool_search_call', 'execution': 'client', 'call_id': 'search_' + str(step), 'status': 'completed', 'arguments': {'query': 'reach_host ' + name, 'limit': 3}}
                    else:
                        item = {'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'P3_TERMINAL_VERIFIED'}]}
                    event('response.output_item.done', {'type': 'response.output_item.done', 'item': item})
                    event('response.completed', {'type': 'response.completed', 'response': {'id': 'p3', 'usage': {'input_tokens': 0, 'input_tokens_details': None, 'output_tokens': 0, 'output_tokens_details': None, 'total_tokens': 0}}})
                else:
                    event('message_start', {'type': 'message_start', 'message': {'id': 'p3', 'type': 'message', 'role': 'assistant', 'content': [], 'model': 'reach-mock', 'stop_reason': None, 'stop_sequence': None, 'usage': {'input_tokens': 0, 'output_tokens': 0}}})
                    if name:
                        if name in found:
                            full_name = found[name][0]
                        elif any(t.get('name') == 'Bash' for t in candidates):
                            # Claude discovers MCP asynchronously. Exercise its ordinary
                            # Guest Bash route while waiting, without advancing the terminal plan.
                            full_name, arguments = 'Bash', {'command': 'printf P3_BRIDGE_WAIT; hostname'}
                        else:
                            event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': 'READY'}})
                            event('content_block_stop', {'type': 'content_block_stop', 'index': 0})
                            event('message_delta', {'type': 'message_delta', 'delta': {'stop_reason': 'end_turn', 'stop_sequence': None}, 'usage': {'output_tokens': 0}})
                            event('message_stop', {'type': 'message_stop'})
                            return
                        call_id = 'p3_' + str(step) + '_' + str(reads)
                        event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'tool_use', 'id': call_id, 'name': full_name, 'input': {}}})
                        event('content_block_delta', {'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'input_json_delta', 'partial_json': json.dumps(arguments)}})
                        stop = 'tool_use'
                        if name in found:
                            waiting, pending_id = True, call_id
                    else:
                        event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': ''}})
                        event('content_block_delta', {'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'text_delta', 'text': 'P3_TERMINAL_VERIFIED'}})
                        stop = 'end_turn'
                    event('content_block_stop', {'type': 'content_block_stop', 'index': 0})
                    event('message_delta', {'type': 'message_delta', 'delta': {'stop_reason': stop, 'stop_sequence': None}, 'usage': {'output_tokens': 0}})
                    event('message_stop', {'type': 'message_stop'})
            except Exception as error:
                failures.append(str(error))
                raise

    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix='reach-p3-terminal-') as temp:
            home = Path(temp); bins = home / 'bin'; bins.mkdir()
            url = 'http://127.0.0.1:' + str(server.server_port)
            env = {'PATH': str(bins) + ':/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': temp, 'REACH_HOME': state, 'LANG': 'C', 'LC_ALL': 'C', 'DO_NOT_TRACK': '1'}
            if args.harness == 'codex':
                source = home / 'empty-codex'; source.mkdir()
                env.update(CODEX_HOME=str(source), OPENAI_API_KEY='dummy-offline-conformance')
                flags = ['-c', 'model_providers.p3.name="offline"', '-c', 'model_providers.p3.base_url=' + json.dumps(url), '-c', 'model_providers.p3.wire_api="responses"', '-c', 'model_providers.p3.requires_openai_auth=false', '-c', 'model_providers.p3.supports_websockets=false', '-c', 'model_provider="p3"']
                invoke = 'exec ' + shlex.join([args.binary] + flags)
                wrapper = bins / 'codex'
                wrapper.write_text('#!/bin/sh\nfor arg do\n if [ "$arg" = -a ]; then ' + invoke + ' "$@"; fi\ndone\n' + invoke + ' -a never "$@"\n'); wrapper.chmod(0o700)
                words = ['exec', '--ephemeral', '--skip-git-repo-check', '--model', 'gpt-5.4']
            else:
                wrapper = bins / 'claude'
                wrapper.write_text('#!/bin/sh\nexec ' + shlex.quote(args.binary) + ' --debug-file ' + shlex.quote(str(evidence / 'claude-debug.log')) + ' "$@"\n'); wrapper.chmod(0o700)
                env.update(ANTHROPIC_API_KEY='dummy-offline-conformance', ANTHROPIC_BASE_URL=url, CLAUDE_TELEMETRY_OPT_OUT='1', ENABLE_TOOL_SEARCH='false')
                words = ['--print', '--no-session-persistence', '--model', 'reach-mock']
            (home / '.mcp.json').write_text(json.dumps({'mcpServers': {'evil': {'command': '/usr/bin/touch', 'args': [str(home / 'injected-host-canary')]}}}))
            with (evidence / 'launcher.log').open('w') as output:
                proc = subprocess.run([args.reach, '--project', args.project, args.harness, *words, 'Follow tool instructions exactly.'], env=env, cwd=temp, stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT, timeout=100)
            result = {'harness': args.harness, 'binary_sha256': hashlib.sha256(Path(args.binary).read_bytes()).hexdigest(), 'exit': proc.returncode, 'terminal_id': args.terminal_id, 'expected': args.expected, 'steps': observed, 'errors': failures, 'next_cursor': cursor, 'catalog': catalog}
            (evidence / 'result.json').write_text(json.dumps(result, indent=2))
            assert not (home / 'injected-host-canary').exists(), 'injected Host MCP dispatched'
            assert proc.returncode == 0 and step == 4 and not failures, (proc.returncode, step, failures, (evidence / 'launcher.log').read_text()[-5000:])
            print(args.harness, 'terminal attach/write/Enter/read passed; persistent value', args.expected)
    finally:
        server.shutdown()


if __name__ == '__main__':
    run()
