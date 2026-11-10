import base64
import json
import pathlib
import queue
import re
import shlex
import subprocess
import time
import threading
import uuid

root = pathlib.Path.home() / '.config/fennec'
runtime = json.loads((root / 'takeover-preferences.json').read_text())['runtime_id']
bridge = pathlib.Path(__file__).with_name('terminal-channel-prototype.py').read_bytes()
command = "python3 -u -c " + shlex.quote('import base64;exec(base64.b64decode(%r))' % base64.b64encode(bridge).decode())
started = time.monotonic()
process = subprocess.Popen(['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(root / 'cloud/known_hosts'), '-i', str(root / 'cloud/runtime_key'), runtime + '@ssh.runta.com', command], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1)
assert json.loads(process.stdout.readline())['type'] == 'ready'
print(json.dumps({'prototype_connection_ready_ms': (time.monotonic() - started) * 1000}), flush=True)
messages = queue.Queue(maxsize=64)


def receive():
    for line in process.stdout:
        messages.put(json.loads(line))
    messages.put(None)


threading.Thread(target=receive, daemon=True).start()
responses = {b'\x1b]10;?': b'\x1b]10;rgb:cccc/cccc/cccc\x1b\\', b'\x1b]11;?': b'\x1b]11;rgb:1818/1818/1818\x1b\\', b'\x1b]12;?': b'\x1b]12;rgb:cccc/cccc/cccc\x1b\\', b'\x1b[16t': b'\x1b[6;16;8t', b'\x1b[c': b'\x1b[?1;2c', b'\x1b[6n': b'\x1b[1;1R'}


def send(message):
    process.stdin.write(json.dumps(message) + '\n')
    process.stdin.flush()


try:
    for sample in range(3):
        identity = uuid.uuid4().hex
        guest = '''import os,pathlib,subprocess,json,tempfile,signal,shutil
signal.signal(signal.SIGHUP,lambda *args: (_ for _ in ()).throw(SystemExit(1)))
home=pathlib.Path.home()
hc=str(home/'.local/bin/herdr-core')
cwd=tempfile.mkdtemp(prefix='fennec-channel-profile-')
terminal=None
try:
 environment=dict(os.environ,PATH=str(home/'.local/fennec/node/bin')+':'+str(home/'.local/fennec/agents/bin')+':'+str(home/'.local/bin')+':'+os.environ['PATH'])
 command=[shutil.which('codex',path=environment['PATH']),'--no-daemon','--no-alt-screen','-c','sandbox_mode="danger-full-access"','-c','approval_policy="never"']
 terminal=json.loads(subprocess.check_output([hc,'run','--bg','--no-parent','--outlive-parent','--json','--cwd',cwd,'--label','fennec.profile='+%r,'--']+command,env=environment))
 subprocess.run([hc,'attach',terminal['address']],env=environment)
finally:
 if terminal:
  current=json.loads(subprocess.check_output([hc,'get',terminal['address']]))
  if current.get('labels',{}).get('fennec.profile')!=%r:raise SystemExit('test session identity changed')
  if current.get('state')!='finished':subprocess.run([hc,'kill',terminal['address']],stdout=subprocess.DEVNULL,check=True)
 shutil.rmtree(cwd)
''' % (identity, identity)
        script = 'python3 -u -c ' + shlex.quote(guest)
        launch_started = time.monotonic()
        send({'type': 'launch', 'id': identity, 'script': script, 'rows': 40, 'columns': 120})
        output = b''
        screen = None
        while time.monotonic() - launch_started < 30:
            try:
                message = messages.get(timeout=1)
            except queue.Empty:
                continue
            if message is None:
                raise RuntimeError('prototype channel closed')
            if message.get('id') != identity:
                continue
            if message['type'] == 'data':
                chunk = base64.b64decode(message['data'])
                output += chunk
                for query, response in responses.items():
                    if query in chunk:
                        send({'type': 'input', 'id': identity, 'data': base64.b64encode(response).decode()})
                plain = re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]', b'', output)
                if screen is None and b'OpenAI Codex' in plain and '›'.encode() in plain:
                    screen = (time.monotonic() - launch_started) * 1000
                    send({'type': 'input', 'id': identity, 'data': base64.b64encode(b'\x04').decode()})
            elif message['type'] == 'exit':
                print(json.dumps({'sample': sample + 1, 'prototype_usable_screen_ms': screen, 'exit': message['code']}), flush=True)
                assert screen is not None and message['code'] == 0
                break
            elif message['type'] == 'error':
                raise RuntimeError(message['message'])
        else:
            send({'type': 'close', 'id': identity})
            raise RuntimeError('prototype session did not complete before timeout')
finally:
    process.stdin.close()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.terminate()
        process.wait(timeout=5)
