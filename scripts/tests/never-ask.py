"""Exercise durable, per-device approval using real OpenSSH and isolated keys."""
import json
import os
import pathlib
import queue
import subprocess
import tempfile
import threading

binary = os.environ.get('FENNEC_TEST_BINARY', 'build/Fennec.app/Contents/MacOS/fennec-daemon')
with tempfile.TemporaryDirectory(prefix='fennec-trust-') as directory:
    root = pathlib.Path(directory)
    for name in ('first', 'second'):
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(root / name)], check=True)
    paired = root / 'authorized_keys'
    paired.write_bytes((root / 'first.pub').read_bytes() + (root / 'second.pub').read_bytes())
    paired.chmod(0o600)

    def start():
        process = subprocess.Popen([binary, 'connector', '-state-dir', directory], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        events = queue.Queue()
        def read():
            for line in process.stdout:
                events.put(json.loads(line))
        threading.Thread(target=read, daemon=True).start()
        address = events.get(timeout=5)['address']
        return process, events, address.rsplit(':', 1)[1]

    def request(port, name):
        return subprocess.Popen(['ssh', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=accept-new',
                                 '-o', 'UserKnownHostsFile=' + str(root / 'known_hosts'), '-i', str(root / name), '-p', port,
                                 'fennec@127.0.0.1', 'printf remembered; exit 7'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

    def decision(process, events, allow, remember=False):
        while True:
            event = events.get(timeout=5)
            if event['type'] == 'approval':
                break
        process.stdin.write(json.dumps({'id': event['id'], 'allow': allow, 'never_ask': remember}) + '\n')
        process.stdin.flush()

    def result(client, code, expected):
        try:
            out, err = client.communicate(timeout=8)
        except subprocess.TimeoutExpired:
            client.kill()
            client.communicate()
            raise AssertionError('command waited unexpectedly')
        assert client.returncode == code, (client.returncode, err)
        assert out == expected, out

    daemon, events, port = start()
    try:
        client = request(port, 'first')
        decision(daemon, events, True, True)
        result(client, 7, 'remembered')
        result(request(port, 'first'), 7, 'remembered')
        client = request(port, 'second')
        decision(daemon, events, False)
        result(client, 126, '')
        print('PASS Never Ask grants only selected device')
    finally:
        daemon.stdin.close()
        daemon.wait(timeout=5)
    daemon, events, port = start()
    try:
        result(request(port, 'first'), 7, 'remembered')
        daemon.stdin.write(json.dumps({'action': 'clear_trust'}) + '\n')
        daemon.stdin.flush()
        while True:
            event = events.get(timeout=5)
            if event['type'] in ('trust_cleared', 'trust_clear_failed'):
                assert event['type'] == 'trust_cleared', event
                break
        client = request(port, 'first')
        decision(daemon, events, False)
        result(client, 126, '')
        print('PASS trust reset requires approval for previously remembered device')
        client = request(port, 'first')
        decision(daemon, events, True, True)
        result(client, 7, 'remembered')
        paired.write_bytes((root / 'second.pub').read_bytes())
        result(request(port, 'first'), 255, '')
        print('PASS trust survives restart but never overrides revoked pairing')
    finally:
        daemon.stdin.close()
        daemon.wait(timeout=5)
