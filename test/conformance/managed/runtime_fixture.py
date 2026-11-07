#!/usr/bin/env python3
"""Explicit P5 operator fixture provisioning. No pull/build, global changes or cleanup.

Creates only newly named, task-labelled resources and records every exact ID.
Run only after native external-volume verification and with Guest-built binaries.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile


def main():
    parser = argparse.ArgumentParser()
    for name in ['root', 'control', 'reach', 'helper', 'task']:
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--resume', action='store_true', help='verify and continue exact IDs from an incomplete manifest')
    args = parser.parse_args()
    root, control = Path(args.root), Path(args.control)
    assert root.is_relative_to('/Volumes/MPM/MCACHES/agentreach')
    assert control.is_relative_to('/Volumes/APAP/MCACHES/agentreach')
    manifest = root / 'evidence' / 'resources.json'
    assert manifest.exists() == args.resume, 'Existing provisioning requires explicit --resume'
    assert Path('/Volumes/MPM').is_mount() and Path('/Volumes/APAP').is_mount()
    if not args.resume:
        control.mkdir(mode=0o700, parents=False, exist_ok=False)
    env = dict(os.environ, REACH_HOME=str(control))
    docker = ['docker', '--context', 'orbstack']
    image = 'sha256:c9f6a16b80584a752f2e3d06a32b0882e3e3d7bfd934ee1d2f624b560f9e1183'
    records = json.loads(manifest.read_text()) if args.resume else {'task': args.task, 'state': str(control), 'resources': {}, 'commands': []}
    assert records['task'] == args.task and records['state'] == str(control) and not records.get('complete')

    def save():
        manifest.write_text(json.dumps(records, indent=2))

    def run(argv, stdin=None):
        p = subprocess.run(argv, input=stdin, capture_output=True, text=not isinstance(stdin, bytes), env=env, timeout=45)
        stdout = p.stdout.decode() if isinstance(p.stdout, bytes) else p.stdout
        stderr = p.stderr.decode() if isinstance(p.stderr, bytes) else p.stderr
        records['commands'].append({'argv': argv, 'exit': p.returncode, 'stdout': stdout, 'stderr': stderr})
        save()
        assert p.returncode == 0, (argv, p.stdout, p.stderr)
        return stdout.strip()

    volumes = []
    for mount, uuid in [('MPM', 'C48A19AC-6C78-34CA-8969-BD408C67CAFE'), ('APAP', 'D1814C29-318B-418F-B5F6-1F7E324E5B42')]:
        sentinel = (root if mount == 'MPM' else control.parent) / ('p5-' + mount + '-sentinel')
        if not args.resume:
            with sentinel.open('xb') as f:
                f.write(os.urandom(32))
        sentinel.chmod(0o600)
        volumes.append({'path': '/Volumes/' + mount, 'uuid': uuid, 'name': mount, 'sentinel': str(sentinel), 'sentinel_sha256': hashlib.sha256(sentinel.read_bytes()).hexdigest()})
    for mode in ['shared', 'owned']:
        project = 'p5-' + mode + '-01a1b418'
        r = records['resources'].get(mode, {'project': project, 'image': image})
        assert r['project'] == project and r['image'] == image
        records['resources'][mode] = r
        labels = ['--label', 'agentreach.task=' + args.task, '--label', 'dev.agentreach.managed=true', '--label', 'dev.agentreach.project=' + project]
        network = r.get('network_id') or run(docker + ['network', 'create', '--driver', 'bridge'] + labels + ['reach-' + project])
        net = json.loads(run(docker + ['network', 'inspect', network]))[0]
        assert net['Id'] == network and net['Labels']['agentreach.task'] == args.task
        r['network_id'] = network
        save()
        guest = '/workspace/project'
        mounts, binds, named = [], [], []
        workspace = {'mode': 'shared-project' if mode == 'shared' else 'guest-owned', 'guest_path': guest}
        if mode == 'shared':
            directory = root / 'shared-workspace'
            directory.mkdir(exist_ok=args.resume)
            workspace['host_path'] = str(directory)
            binds = [{'source': str(directory), 'target': guest, 'read_only': False}]
            mounts = ['--mount', 'type=bind,src=' + str(directory) + ',dst=' + guest]
        else:
            volume = r.get('volume') or run(docker + ['volume', 'create'] + labels + ['reach-' + project])
            v = json.loads(run(docker + ['volume', 'inspect', volume]))[0]
            assert v['Name'] == volume and v['Labels']['agentreach.task'] == args.task
            r['volume'] = volume
            save()
            named = [{'name': volume, 'target': guest, 'read_only': False}]
            mounts = ['--mount', 'type=volume,src=' + volume + ',dst=' + guest + ',volume-nocopy']
        cid = r.get('id') or run(docker + ['create', '--name', 'reach-' + project, '--network', network, '--user', '10001:10001', '--workdir', guest,
                            '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--init', '--restart', 'no', '--memory', '512m', '--memory-swap', '512m', '--cpus', '1', '--pids-limit', '128', '--log-opt', 'max-size=4m', '--log-opt', 'max-file=1'] + labels + mounts + [image, 'sleep', 'infinity'])
        r['id'] = cid
        save()
        actual = json.loads(run(docker + ['inspect', cid]))[0]
        assert actual['Id'] == cid and actual['Config']['Labels']['agentreach.task'] == args.task
        assert not actual['HostConfig']['Privileged'] and not actual['HostConfig']['PortBindings']
        if not actual['State']['Running']:
            run(docker + ['start', cid])
        run(docker + ['cp', args.helper, cid + ':/tmp/p5-reach-helper'])
        run(docker + ['exec', '--user', str(os.getuid()), cid, '/usr/bin/chmod', '0755', '/tmp/p5-reach-helper'])
        run(docker + ['exec', '--user', '0', cid, '/usr/bin/install', '-m', '0755', '/tmp/p5-reach-helper', '/usr/local/bin/reach-helper'])
        if mode == 'owned':
            # The daemon extracts an explicit directory owner; no CHOWN or DAC
            # capability is added to the permanently restricted workspace.
            archive = io.BytesIO()
            with tarfile.open(fileobj=archive, mode='w') as tar:
                info = tarfile.TarInfo('project')
                info.type, info.uid, info.gid, info.mode = tarfile.DIRTYPE, 10001, 10001, 0o755
                tar.addfile(info)
            run(docker + ['cp', '--archive', '-', cid + ':/workspace'], archive.getvalue())
            assert run(docker + ['exec', cid, '/usr/bin/stat', '-c', '%u:%g:%a', guest]) == '10001:10001:755'
        run(docker + ['exec', cid, '/usr/local/bin/reach-helper', '--selftest'])
        actual_hash = run(docker + ['exec', cid, '/usr/bin/sha256sum', '/usr/local/bin/reach-helper']).split()[0]
        assert actual_hash == hashlib.sha256(Path(args.helper).read_bytes()).hexdigest()
        spec = {'schema': 1, 'id': project, 'provider': 'orbstack-docker',
                'docker': {'cli': os.path.realpath('/usr/local/bin/docker'), 'context': 'orbstack', 'endpoint': 'unix:///Users/nyan/.orbstack/run/docker.sock', 'config_dir': '/Users/nyan/.docker', 'control_home': '/Users/nyan'},
                'container': cid, 'workspace': workspace, 'engine_data_path': '/Volumes/APAP/vm/Orbstacks', 'orbstack_config': '/Users/nyan/.orbstack/vmconfig.json', 'volumes': volumes, 'bind_mounts': binds, 'named_volumes': named}
        specfile = control / (project + '.json')
        specfile.write_text(json.dumps(spec))
        specfile.chmod(0o600)
        if not (control / 'projects' / (project + '.json')).exists():
            run([args.reach, 'project', 'register', str(specfile)])
        registered = json.loads(run([args.reach, 'project', 'inspect', project]))
        assert registered['binding']['container_id'] == cid
        if not (control / 'security' / (project + '.json')).exists():
            run([args.reach, 'policy', 'init', project])
        for cap in ['runtime_change', 'service_change', 'port_expose', 'open_url']:
            policy = json.loads(run([args.reach, 'policy', 'inspect', project]))
            if policy['host'].get(cap, 'deny') != 'approval':
                run([args.reach, 'policy', 'set', project, cap, 'approval'])
        run([args.reach, '--project', project, 'exec', '--', 'printf P5_FIXTURE_READY'])
        print(mode, cid, network, flush=True)
    records['complete'] = True
    save()


if __name__ == '__main__':
    main()
