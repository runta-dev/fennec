import argparse
import fcntl
import json
import os
import pathlib
import pty
import re
import select
import struct
import subprocess
import termios
import time

parser = argparse.ArgumentParser()
parser.add_argument('--samples', type=int, default=3)
parser.add_argument('--binary', required=True)
parser.add_argument('--mode', choices=['cold', 'warm'], default='cold')
parser.add_argument('--exercise-input', action='store_true')
arguments = parser.parse_args()
directory = pathlib.Path.home() / '.config/fennec'
marker = directory / 'launch-profile.enabled'
profile = directory / 'launch-profile.jsonl'
if marker.exists():
    raise SystemExit('profiling already enabled; inspect existing run')
marker.touch(mode=0o600)
try:
    for sample in range(arguments.samples):
        before = set((directory / 'launches').glob('*.json'))
        started = time.monotonic()
        started_wall = time.time() * 1000
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
        environment = dict(os.environ, TERM='xterm-256color')
        if arguments.mode == 'cold':
            environment['FENNEC_COLD_START'] = '1'
        else:
            environment.pop('FENNEC_COLD_START', None)
        environment.pop('NO_COLOR', None)
        process = subprocess.Popen([arguments.binary, 'run-agent', 'codex', '--no-alt-screen'], stdin=slave, stdout=slave, stderr=slave, env=environment)
        os.close(slave)
        output = b''
        screen = None
        responses = {
            b'\x1b]10;?': b'\x1b]10;rgb:cccc/cccc/cccc\x1b\\',
            b'\x1b]11;?': b'\x1b]11;rgb:1818/1818/1818\x1b\\',
            b'\x1b]12;?': b'\x1b]12;rgb:cccc/cccc/cccc\x1b\\',
            b'\x1b[16t': b'\x1b[6;16;8t',
            b'\x1b[c': b'\x1b[?1;2c',
            b'\x1b[6n': b'\x1b[1;1R',
        }
        deadline = started + 90
        while time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                output += chunk
                for query, response in responses.items():
                    if query in chunk:
                        os.write(master, response)
                plain = re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]', b'', output)
                if b'OpenAI Codex' in plain and '›'.encode() in plain:
                    screen = (time.monotonic() - started) * 1000
                    break
            if process.poll() is not None:
                break
        input_exit = None
        if arguments.exercise_input and screen is not None:
            os.write(master, b'\x04')
            input_deadline = time.monotonic() + 10
            while process.poll() is None and time.monotonic() < input_deadline:
                readable, _, _ = select.select([master], [], [], 0.1)
                if readable:
                    try:
                        os.read(master, 65536)
                    except OSError:
                        break
            try:
                input_exit = process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                pass
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        os.close(master)
        created = set((directory / 'launches').glob('*.json')) - before
        receipt = json.loads(next(iter(created)).read_text()) if len(created) == 1 else None
        if receipt:
            ssh = ['ssh', '-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(directory / 'cloud/known_hosts'), '-i', str(directory / 'cloud/runtime_key'), receipt['runtime_id'] + '@ssh.runta.com', 'python3 -']
            cleanup = "import json,subprocess,pathlib\nhc=str(pathlib.Path.home()/'.local/bin/herdr-core')\nt=json.loads(subprocess.check_output([hc,'get',%r]))\nassert t.get('labels',{}).get('fennec.session')==%r\nif t.get('state')!='finished':subprocess.run([hc,'kill',%r],check=True,stdout=subprocess.DEVNULL)\n" % (receipt['address'], receipt['session_id'], receipt['address'])
            subprocess.run(ssh, input=cleanup, text=True, check=True, timeout=30)
        events = [json.loads(line) for line in profile.read_text().splitlines()] if profile.exists() else []
        events = [event for event in events if event['started_unix_ms'] >= started_wall - 1]
        broker = next((event for event in events if event['pid'] == process.pid and event['stage'] == 'broker.request'), None)
        launch = next((event for event in events if event['stage'] in ('session.launch', 'plan.prepare')), None)
        selected = [event for event in events if event['pid'] in (process.pid, launch['pid'] if launch else None)]
        print(json.dumps({'mode': arguments.mode, 'sample': sample + 1, 'screen_ms': screen, 'input_exit_code': input_exit, 'broker_ms': broker['elapsed_ms'] if broker else None, 'after_broker_to_screen_ms': screen - broker['elapsed_ms'] if screen is not None and broker else None, 'events': selected}), flush=True)
        if screen is None:
            raise SystemExit('TUI first screen not observed')
        if arguments.exercise_input and input_exit != 0:
            raise SystemExit('TUI input/exit was not confirmed')
finally:
    marker.unlink(missing_ok=True)
