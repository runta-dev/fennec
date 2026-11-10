import json,os,pathlib,subprocess,tempfile,socket,threading
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory(prefix='fn-',dir='/tmp') as d:
 home=pathlib.Path(d);tools=home/'tools';tools.mkdir();original=tools/'codex'
 original.write_text('#!/bin/sh\nif [ \"$1\" = --version ]; then echo \"codex-cli 0.166.0\"; exit 0; fi\nprintf "local:%s" "$1"\nexit 9\n');original.chmod(0o700)
 env={**os.environ,'HOME':d,'PATH':str(tools)+':/usr/bin:/bin'}
 result=subprocess.run([binary,'install-shims'],env=env,text=True,capture_output=True,check=True)
 shims=json.loads(result.stdout)['directory'];env['PATH']=shims+':'+env['PATH']
 result=subprocess.run(['codex','literal $(no)'],env=env,capture_output=True,text=True,timeout=5)
 assert result.returncode==9 and result.stdout=='local:literal $(no)',(result.returncode,result.stdout,result.stderr)
 config=home/'.config/fennec';config.mkdir(parents=True);(config/'takeover.json').write_text('{"agents":["codex"]}')
 result=subprocess.run(['codex','hello'],env=env,capture_output=True,text=True,timeout=5)
 assert result.returncode!=0 and not result.stdout and 'broker unavailable' in result.stderr,result.stderr
 print('PASS real PATH shim preserves local execution; enabled takeover never silently falls back')

 # Exercise the real shim -> run-agent -> Unix socket protocol without cloud effects.
 ipc=config/'ipc';ipc.mkdir(mode=0o700)
 server=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM)
 # Short private path avoids the macOS sockaddr_un limit.
 address=str(ipc/'launch.sock')
 server.bind(address);server.listen(1);server.settimeout(8)
 received=[]
 def broker():
  connection,_=server.accept()
  with connection:
   stream=connection.makefile('rb')
   received.append(json.loads(stream.readline()))
   connection.sendall(b'{"error":"acceptance stopped before cloud launch"}\n')
 worker=threading.Thread(target=broker);worker.start()
 try:
  result=subprocess.run(['codex','literal $(no)'],env=env,capture_output=True,text=True,timeout=8)
  worker.join(timeout=8)
  assert not worker.is_alive()
  assert received and received[0]['version']=='0.166.0',received
  assert received[0]['arguments']==['literal $(no)'],received
  assert 'acceptance stopped before cloud launch' in result.stderr,result.stderr
  print('PASS real shim sends terminal executable version and literal arguments to broker')
 finally:
  server.close()
