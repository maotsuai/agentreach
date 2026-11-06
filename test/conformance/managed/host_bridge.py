#!/usr/bin/env python3
"""Real managed launcher / MCP conformance; localhost models and dummy auth only.

Requires an explicitly registered isolated project with initialized policy.
The script grants only its own exact test plan, through the Host operator CLI.
It never installs tools or calls a paid model provider.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def run():
    parser = argparse.ArgumentParser()
    parser.add_argument('--reach', required=True)
    parser.add_argument('--binary', required=True)
    parser.add_argument('--project', required=True)
    parser.add_argument('--harness', choices=['claude', 'codex'], required=True)
    parser.add_argument('--evidence', required=True)
    args = parser.parse_args()
    state = os.environ['REACH_HOME']
    evidence = Path(args.evidence)
    evidence.mkdir(parents=True, exist_ok=True)
    observed, catalog, approvals = [], [], []
    plan, approval = {}, {}
    step = 0
    waiting = False
    failures = []

    def operator(*words):
        env = {'PATH': '/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': str(Path.home()), 'REACH_HOME': state}
        p = subprocess.run([args.reach, 'policy', *words], env=env, capture_output=True, text=True, timeout=40)
        if p.returncode:
            raise AssertionError(p.stderr)
        return json.loads(p.stdout)

    operator('set', args.project, 'open_url', 'approval')
    expected = ['policy_revision', 'request_id', 'approval_required', 'policy_denied', 'unknown field', 'exposure_unavailable']

    def tools_in(items, namespace=None):
        found = {}
        for item in items:
            if item.get('type') == 'namespace':
                found.update(tools_in(item.get('tools', []), item.get('name')))
            else:
                for name in ['host_status', 'host_plan', 'host_execute']:
                    if item.get('name', '').endswith(name):
                        found[name] = (item['name'], namespace)
        return found

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            nonlocal step, waiting, plan, approval
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            if self.path.endswith('/count_tokens'):
                self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(b'{\"input_tokens\":0}'); return
            candidates = list(request.get('tools', []))
            outputs = []
            if args.harness == 'codex':
                for item in request.get('input', []):
                    if item.get('type') in ['tool_search_output', 'additional_tools']:
                        candidates.extend(item.get('tools', []))
                    if item.get('type') == 'function_call_output':
                        outputs.append(str(item.get('output', '')))
            else:
                for message in request.get('messages', []):
                    if isinstance(message.get('content'), list):
                        outputs.extend(x.get('content', '') for x in message['content'] if x.get('type') == 'tool_result')
            catalog.append(candidates)
            (evidence / 'last-discovery.json').write_text(json.dumps({'input': [x for x in request.get('input', []) if x.get('type') in ['tool_search_output', 'additional_tools']], 'step': step}, indent=2))
            (evidence / 'catalog.json').write_text(json.dumps(catalog, indent=2) + '\n')
            try:
                if waiting and outputs:
                    result = str(outputs[-1])
                    observed.append({'step': step, 'result': result})
                    assert expected[step] in result, result
                    # Tool output is a JSON MCP content envelope in Codex, and
                    # a content array in Claude. Find the nested typed payload.
                    def payload(value):
                        if isinstance(value, str):
                            try:
                                return payload(json.loads(value.split('\nOutput:\n', 1)[-1]))
                            except (ValueError, TypeError):
                                return None
                        if isinstance(value, dict):
                            if 'request_id' in value and 'request_digest' in value:
                                return value
                            for child in value.values():
                                p = payload(child)
                                if p:
                                    return p
                        if isinstance(value, list):
                            for child in value:
                                p = payload(child)
                                if p:
                                    return p
                        return None
                    if step == 1:
                        plan = payload(outputs[-1])
                        assert plan, result
                    if step == 2:
                        approval = operator('approve', args.project, plan['request_id'], plan['request_digest'])
                        approvals.append(approval)
                    step += 1
                    waiting = False
                found = tools_in(candidates)
                # No operator or injected tools may be in the Host namespace.
                encoded = json.dumps(candidates)
                for forbidden in ['mcp__reach_host__approve', 'mcp__reach_host__revoke', 'mcp__evil__']:
                    assert forbidden not in encoded, forbidden
                name = ['host_status', 'host_plan', 'host_execute', 'host_plan', 'host_plan', 'host_execute'][step] if step < 6 else None
                arguments = {}
                if step in [1, 3, 4]:
                    arguments = {'capability': 'open_url' if step != 3 else 'arbitrary_host_shell', 'target_id': 'p2-test-exposure', 'generation': 1, 'params': {'path': '/'}}
                    if step == 4:
                        arguments['command'] = 'forbidden-host-command'
                elif step in [2, 5]:
                    arguments = {'request_id': plan['request_id']}
                    if step == 5:
                        arguments['approval_id'] = approval['approval_id']
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.send_header('Connection', 'close')
                self.end_headers()

                def event(name, value):
                    self.wfile.write(('event: ' + name + '\ndata: ' + json.dumps(value) + '\n\n').encode())
                    self.wfile.flush()

                if args.harness == 'codex':
                    event('response.created', {'type': 'response.created', 'response': {'id': 'p2'}})
                    if name and name in found:
                        full, namespace = found[name]
                        item = {'type': 'function_call', 'name': full, 'call_id': 'p2_' + str(step), 'arguments': json.dumps(arguments)}
                        if namespace:
                            item['namespace'] = namespace
                        waiting = True
                    elif name and len(catalog) > 24:
                        failures.append('Host tool discovery exhausted: ' + name)
                        item = {'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'P2_DISCOVERY_FAILED'}]}
                    elif name:
                        item = {'type': 'tool_search_call', 'execution': 'client', 'call_id': 'search_' + str(step), 'status': 'completed', 'arguments': {'query': 'reach_host ' + name, 'limit': 3}}
                    else:
                        item = {'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'P2_BRIDGE_VERIFIED'}]}
                    event('response.output_item.done', {'type': 'response.output_item.done', 'item': item})
                    event('response.completed', {'type': 'response.completed', 'response': {'id': 'p2', 'usage': {'input_tokens': 0, 'input_tokens_details': None, 'output_tokens': 0, 'output_tokens_details': None, 'total_tokens': 0}}})
                else:
                    event('message_start', {'type': 'message_start', 'message': {'id': 'p2', 'type': 'message', 'role': 'assistant', 'content': [], 'model': 'reach-mock', 'stop_reason': None, 'stop_sequence': None, 'usage': {'input_tokens': 0, 'output_tokens': 0}}})
                    if name:
                        if name not in found:
                            if any(t.get('name') == 'Bash' for t in candidates):
                                full_name, arguments = 'Bash', {'command': 'printf P2_BRIDGE_WAIT; hostname'}
                            else:
                                full_name = None
                        else:
                            full_name = found[name][0]
                        if full_name is None:
                            event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': 'READY'}})
                            event('content_block_stop', {'type': 'content_block_stop', 'index': 0})
                            event('message_delta', {'type': 'message_delta', 'delta': {'stop_reason': 'end_turn', 'stop_sequence': None}, 'usage': {'output_tokens': 0}})
                            event('message_stop', {'type': 'message_stop'}); return
                        event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'tool_use', 'id': 'p2_' + str(step), 'name': full_name, 'input': {}}})
                        event('content_block_delta', {'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'input_json_delta', 'partial_json': json.dumps(arguments)}})
                        stop = 'tool_use'
                        waiting = name in found
                    else:
                        event('content_block_start', {'type': 'content_block_start', 'index': 0, 'content_block': {'type': 'text', 'text': ''}})
                        event('content_block_delta', {'type': 'content_block_delta', 'index': 0, 'delta': {'type': 'text_delta', 'text': 'P2_BRIDGE_VERIFIED'}})
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
        with tempfile.TemporaryDirectory(prefix='reach-p2-bridge-') as temp:
            home = Path(temp)
            bins = home / 'bin'
            bins.mkdir()
            url = 'http://127.0.0.1:' + str(server.server_port)
            env = {'PATH': str(bins) + ':/usr/bin:/bin:/usr/sbin:/sbin', 'HOME': temp, 'REACH_HOME': state, 'LANG': 'C', 'LC_ALL': 'C', 'DO_NOT_TRACK': '1'}
            if args.harness == 'codex':
                source = home / 'empty-codex'
                source.mkdir()
                (source / 'config.toml').write_text('[mcp_servers.evil]\ncommand = \"/usr/bin/touch\"\nargs = [' + json.dumps(str(home / 'injected-host-canary')) + ']\n')
                env.update(CODEX_HOME=str(source), OPENAI_API_KEY='dummy-offline-conformance')
                flags = ['-c', 'model_providers.p2.name="offline"', '-c', 'model_providers.p2.base_url=' + json.dumps(url), '-c', 'model_providers.p2.wire_api="responses"', '-c', 'model_providers.p2.requires_openai_auth=false', '-c', 'model_providers.p2.supports_websockets=false', '-c', 'model_provider="p2"']
                invoke = 'exec ' + shlex.join([args.binary] + flags)
                wrapper = bins / 'codex'
                wrapper.write_text('#!/bin/sh\nfor arg do\n if [ "$arg" = -a ]; then ' + invoke + ' "$@"; fi\ndone\n' + invoke + ' -a never "$@"\n')
                wrapper.chmod(0o700)
                words = ['exec', '--ephemeral', '--skip-git-repo-check', '--model', 'gpt-5.4']
            else:
                wrapper = bins / 'claude'
                wrapper.write_text('#!/bin/sh\nexec ' + shlex.quote(args.binary) + ' --debug-file ' + shlex.quote(str(evidence / 'claude-debug.log')) + ' \"$@\"\n')
                wrapper.chmod(0o700)
                env.update(ANTHROPIC_API_KEY='dummy-offline-conformance', ANTHROPIC_BASE_URL=url, CLAUDE_TELEMETRY_OPT_OUT='1', ENABLE_TOOL_SEARCH='false')
                words = ['--print', '--no-session-persistence', '--model', 'reach-mock']
            # These untrusted project configs must never become Host tools.
            (home / '.mcp.json').write_text(json.dumps({'mcpServers': {'evil': {'command': '/usr/bin/touch', 'args': [str(home / 'injected-host-canary')]}}}))
            with (evidence / 'launcher.log').open('w') as output:
                try:
                    proc = subprocess.run([args.reach, '--project', args.project, args.harness, *words, 'Follow tool instructions exactly.'], env=env, cwd=temp, stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT, text=True, timeout=100)
                except subprocess.TimeoutExpired:
                    (evidence / 'timeout.json').write_text(json.dumps({'step': step, 'observed': observed, 'errors': failures}, indent=2))
                    raise
            launcher_output = (evidence / 'launcher.log').read_text()
            result = {'harness': args.harness, 'binary_sha256': hashlib.sha256(Path(args.binary).read_bytes()).hexdigest(), 'exit': proc.returncode, 'steps': observed, 'approvals': approvals, 'errors': failures, 'catalog': catalog}
            (evidence / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
            assert not (home / 'injected-host-canary').exists(), 'injected Host MCP dispatched'
            assert proc.returncode == 0 and step == 6 and not failures, (proc.returncode, step, failures, launcher_output[-5000:])
            print(args.harness, 'six real MCP roundtrips verified')
    finally:
        server.shutdown()


if __name__ == '__main__':
    run()
