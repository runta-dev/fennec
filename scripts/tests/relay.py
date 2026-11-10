"""Exercise the real relay byte path without cloud credentials."""
import json
import pathlib
import socket
import subprocess
import tempfile
import time

with tempfile.TemporaryDirectory() as directory:
    state = pathlib.Path(directory) / 'state.json'
    process = subprocess.Popen(['python3', 'internal/localssh/assets/relay.py', '--state', str(state)])
    try:
        for _ in range(100):
            if state.exists():
                break
            time.sleep(.02)
        info = json.loads(state.read_text())
        # More abandoned bridges than queue capacity must not prevent reconnect.
        for _ in range(24):
            with socket.create_connection(('127.0.0.1', info['bridge_port'])):
                pass
        time.sleep(.1)
        with socket.create_connection(('127.0.0.1', info['bridge_port'])) as device:
            device.settimeout(3)
            with socket.create_connection(('127.0.0.1', info['agent_port'])) as agent:
                agent.settimeout(3)
                assert device.recv(1) == b'F'
                agent.sendall(b'SSH-2.0-test\r\n')
                assert device.recv(100) == b'SSH-2.0-test\r\n'
                device.sendall(b'SSH-2.0-fennec\r\n')
                assert agent.recv(100) == b'SSH-2.0-fennec\r\n'
                agent.shutdown(socket.SHUT_WR)
                assert device.recv(1) == b''
                device.shutdown(socket.SHUT_WR)
                assert agent.recv(1) == b''
        print('PASS relay bidirectional bytes and half-close')
    finally:
        process.terminate()
        process.wait(timeout=3)
