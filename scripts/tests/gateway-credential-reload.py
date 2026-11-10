import http.server
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request


class Upstream(http.server.BaseHTTPRequestHandler):
    observed = []

    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        self.observed.append(self.headers.get('Authorization'))
        data = json.dumps({'id': 'test', 'object': 'chat.completion', 'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'OK'}, 'finish_reason': 'stop'}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


binary = os.environ.get('FENNEC_TEST_MAGPIE', str(pathlib.Path.home() / '.local/bin/magpie'))
server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
threading.Thread(target=server.serve_forever, daemon=True).start()
with socket.socket() as reservation:
    reservation.bind(('127.0.0.1', 0))
    gateway_port = reservation.getsockname()[1]
with tempfile.TemporaryDirectory(prefix='fennec-gateway-test-') as directory:
    root = pathlib.Path(directory) / '.config/magpie'
    root.mkdir(parents=True)
    def configure(key):
        payload = {'providers': [{'id': 'fixture', 'name': 'Fixture', 'key': key, 'chat': 'http://127.0.0.1:%d/v1' % server.server_port, 'models': ['fixture-model']}], 'groups': []}
        staged = root / 'providers.tmp'
        staged.write_text(json.dumps(payload))
        staged.replace(root / 'providers.json')
    configure('fake-first')
    environment = dict(os.environ, HOME=directory, XDG_CONFIG_HOME=str(pathlib.Path(directory) / '.config'), MAGPIE_ADDR='127.0.0.1:%d' % gateway_port)
    process = subprocess.Popen([binary, 'serve'], env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    base = 'http://127.0.0.1:%d' % gateway_port
    try:
        for attempt in range(50):
            if process.poll() is not None:
                raise RuntimeError('isolated gateway exited')
            try:
                with opener.open(base + '/v1/models', timeout=1) as response:
                    models = json.load(response)
                if any(model['id'] == 'fixture/fixture-model' for model in models['data']):
                    break
            except OSError:
                pass
            time.sleep(0.1)
        else:
            raise RuntimeError('isolated gateway did not expose fixture')
        for key in ('fake-first', 'fake-second'):
            configure(key)
            body = {'model': 'fixture/fixture-model', 'messages': [{'role': 'user', 'content': 'OK'}], 'stream': False, 'max_tokens': 16}
            request = urllib.request.Request(base + '/v1/chat/completions', data=json.dumps(body).encode(), headers={'Content-Type': 'application/json', 'Authorization': 'Bearer magpie'})
            with opener.open(request, timeout=15) as response:
                assert json.load(response)['choices']
            assert Upstream.observed[-1] == 'Bearer ' + key, 'gateway used stale credentials'
        print('PASS same gateway process loaded both credential revisions')
    finally:
        process.terminate()
        process.wait(timeout=10)
        server.shutdown()
