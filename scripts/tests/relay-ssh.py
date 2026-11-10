"""Real SSH through the relay to the approval service; isolated temporary keys."""
import json
import os
import pathlib
import select
import socket
import subprocess
import tempfile
import threading
import time

binary = os.environ.get('FENNEC_TEST_BINARY', 'build/Fennec.app/Contents/MacOS/fennec-daemon')
with tempfile.TemporaryDirectory(prefix='fennec-relay-e2e-') as directory:
    root = pathlib.Path(directory)
    subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(root / 'client')], check=True)
    (root / 'authorized_keys').write_bytes((root / 'client.pub').read_bytes())
    (root / 'authorized_keys').chmod(0o600)
    daemon = subprocess.Popen([binary, 'connector', '-require-approval', '-state-dir', directory], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    relay = subprocess.Popen(['python3', 'internal/localssh/assets/relay.py', '--state', str(root / 'relay.json')])
    try:
        listening = json.loads(daemon.stdout.readline())
        local_port = int(listening['address'].rsplit(':', 1)[1])
        for _ in range(100):
            if (root / 'relay.json').exists():
                break
            time.sleep(.02)
        state = json.loads((root / 'relay.json').read_text())

        def bridge():
            with socket.create_connection(('127.0.0.1', state['bridge_port'])) as remote:
                remote.settimeout(10)
                assert remote.recv(1) == b'F'
                with socket.create_connection(('127.0.0.1', local_port)) as local:
                    streams = [remote, local]
                    while streams:
                        ready, _, _ = select.select(streams, [], [], 15)
                        if not ready:
                            raise RuntimeError('bridge stalled')
                        for source in ready:
                            target = local if source is remote else remote
                            data = source.recv(65536)
                            if data:
                                target.sendall(data)
                            else:
                                streams.remove(source)
                                target.shutdown(socket.SHUT_WR)

        for allow in (False, True):
            thread = threading.Thread(target=bridge, daemon=True)
            thread.start()
            command = ['ssh', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=accept-new',
                       '-o', 'UserKnownHostsFile=' + str(root / 'known_hosts'), '-i', str(root / 'client'),
                       '-p', str(state['agent_port']), 'fennec@127.0.0.1', 'printf relay-approved; exit 7']
            client = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            while True:
                if not select.select([daemon.stdout], [], [], 10)[0]:
                    raise RuntimeError('approval did not arrive')
                event = json.loads(daemon.stdout.readline())
                if event['type'] == 'approval':
                    break
            daemon.stdin.write(json.dumps({'id': event['id'], 'allow': allow}) + '\n')
            daemon.stdin.flush()
            output, error = client.communicate(timeout=15)
            assert client.returncode == (7 if allow else 126), error
            assert output == ('relay-approved' if allow else ''), output
            thread.join(timeout=5)
            assert not thread.is_alive(), 'bridge did not close'
            print('PASS relayed SSH', 'allow/output/exit' if allow else 'deny/no execution')
    finally:
        daemon.stdin.close()
        daemon.wait(timeout=5)
        relay.terminate()
        relay.wait(timeout=5)
