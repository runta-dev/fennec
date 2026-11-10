"""Real SSH process cancellation on pairing revocation and parent EOF.

Uses isolated keys/state and the packaged daemon; never changes user pairing.
"""
import json
import os
import pathlib
import queue
import shlex
import subprocess
import tempfile
import threading
import time

binary = os.environ.get('FENNEC_TEST_BINARY', 'build/Fennec.app/Contents/MacOS/fennec-daemon')
with tempfile.TemporaryDirectory(prefix='fennec-cancel-') as directory:
    root = pathlib.Path(directory)
    subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(root / 'client')], check=True)
    paired = root / 'authorized_keys'
    for cause in ('revocation', 'parent_eof'):
        paired.write_bytes((root / 'client.pub').read_bytes())
        paired.chmod(0o600)
        daemon = subprocess.Popen([binary, 'connector', '-state-dir', directory], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        events = queue.Queue()
        def read_events(stream):
            for line in stream:
                events.put(json.loads(line))
        threading.Thread(target=read_events, args=(daemon.stdout,), daemon=True).start()
        port = events.get(timeout=5)['address'].rsplit(':', 1)[1]
        marker = root / (cause + '-heartbeat')
        # The child must stop too: a killed shell alone can leave it running.
        child = 'while :; do printf x >> ' + shlex.quote(str(marker)) + '; sleep 0.1; done'
        command = '/bin/sh -c ' + shlex.quote(child) + ' & wait'
        client = subprocess.Popen(['ssh', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'UserKnownHostsFile=' + str(root / 'known_hosts'), '-i', str(root / 'client'), '-p', port, 'fennec@127.0.0.1', command], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            while True:
                event = events.get(timeout=5)
                if event['type'] == 'approval':
                    break
            assert event['command'] == command
            daemon.stdin.write(json.dumps({'id': event['id'], 'allow': True}) + '\n')
            daemon.stdin.flush()
            deadline = time.monotonic() + 5
            while not marker.exists() and time.monotonic() < deadline:
                time.sleep(.05)
            assert marker.exists(), 'approved child never started'
            if cause == 'revocation':
                paired.write_bytes(b'')
            else:
                daemon.stdin.close()
            client.communicate(timeout=5)
            assert client.returncode != 0, 'cancelled operation reported success'
            size = marker.stat().st_size
            time.sleep(.4)
            assert marker.stat().st_size == size, 'child survived cancellation'
            print('PASS', cause, 'terminates active SSH command and child process')
        finally:
            if client.poll() is None:
                client.kill()
                client.communicate()
            if not daemon.stdin.closed:
                daemon.stdin.close()
            daemon.wait(timeout=5)
