import base64
import fcntl
import json
import os
import pty
import re
import signal
import struct
import subprocess
import sys
import termios
import threading

sessions = {}
sessions_lock = threading.Lock()
output_lock = threading.Lock()


def emit(message):
    with output_lock:
        sys.stdout.write(json.dumps(message, separators=(',', ':')) + '\n')
        sys.stdout.flush()


def launch(message):
    identity = message['id']
    if not re.fullmatch('[a-f0-9]{32}', identity):
        raise ValueError('invalid session identity')
    script = message['script']
    if not isinstance(script, str) or len(script.encode()) > 131072:
        raise ValueError('invalid script')
    with sessions_lock:
        if identity in sessions or len(sessions) >= 16:
            raise ValueError('session already exists or capacity exhausted')
        master, slave = pty.openpty()
        rows = min(1000, max(1, int(message.get('rows', 40))))
        columns = min(1000, max(1, int(message.get('columns', 120))))
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', rows, columns, 0, 0))
        environment = dict(os.environ, TERM='xterm-256color')
        process = subprocess.Popen(['setsid', '--ctty', 'sh', '-c', script], stdin=slave, stdout=slave, stderr=slave, env=environment)
        os.close(slave)
        sessions[identity] = (process, master)
    def output():
        try:
            while True:
                try:
                    data = os.read(master, 16384)
                except OSError:
                    break
                if not data:
                    break
                emit({'type': 'data', 'id': identity, 'data': base64.b64encode(data).decode()})
            code = process.wait()
            emit({'type': 'exit', 'id': identity, 'code': code})
        finally:
            with sessions_lock:
                sessions.pop(identity, None)
            os.close(master)
    threading.Thread(target=output, daemon=True).start()


emit({'type': 'ready'})
try:
    while True:
        line = sys.stdin.buffer.readline(262145)
        if not line:
            break
        if len(line) > 262144:
            raise ValueError('oversized frame')
        message = json.loads(line)
        identity = message.get('id', '')
        try:
            if message['type'] == 'launch':
                launch(message)
                continue
            with sessions_lock:
                session = sessions.get(identity)
            if session is None:
                continue
            process, master = session
            if message['type'] == 'input':
                data = base64.b64decode(message['data'], validate=True)
                if len(data) > 32768:
                    raise ValueError('oversized input')
                os.write(master, data)
            elif message['type'] == 'resize':
                rows = min(1000, max(1, int(message['rows'])))
                columns = min(1000, max(1, int(message['columns'])))
                fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', rows, columns, 0, 0))
            elif message['type'] == 'close':
                os.killpg(process.pid, signal.SIGHUP)
        except (KeyError, ValueError, OSError):
            emit({'type': 'error', 'id': identity, 'message': 'terminal operation failed'})
finally:
    with sessions_lock:
        for process, master in sessions.values():
            try:
                os.killpg(process.pid, signal.SIGHUP)
            except ProcessLookupError:
                pass
