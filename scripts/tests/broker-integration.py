"""Actual broker startup wiring, isolated from real credentials and CLI entries."""
import json,os,pathlib,queue,subprocess,tempfile,threading
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory(prefix='fn-broker-',dir='/tmp') as d:
 home=pathlib.Path(d);tools=home/'bin';tools.mkdir();real=home/'real'
 real.write_text('#!/bin/sh\necho 2.1.318\n');real.chmod(0o700)
 entry=tools/'claude';entry.symlink_to(real)
 env={**os.environ,'HOME':d,'PATH':str(tools)+':/usr/bin:/bin','SHELL':'/bin/bash','XDG_CONFIG_HOME':str(home/'.config'),'ZDOTDIR':d}
 process=subprocess.Popen([binary,'launch-broker'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,env=env)
 events=queue.Queue()
 def read():
  for line in process.stdout:events.put(json.loads(line))
 threading.Thread(target=read,daemon=True).start()
 try:
  sock=home/'ipc/launch.sock'
  process.stdin.write(json.dumps({'token':'unused-test-token','runtime_id':'test-runtime','device':'aabbccdd','socket':str(sock),'agents':['claude'],'selected':['claude','opencode'],'roots':[],'auto_projects':True})+'\n');process.stdin.flush()
  coverage=events.get(timeout=10)
  assert coverage=={'type':'entry_coverage','agent':'claude','coverage':'entry_and_new_shells'},coverage
  assert events.get(timeout=10)=={'type':'broker_ready'}
  assert sock.exists() and not entry.is_symlink()
  assert 'Fennec CLI integration' in (home/'.bashrc').read_text()
  assert json.loads((home/'.config/fennec/takeover.json').read_text())['agents']==['claude','opencode']
  process.stdin.close();process.wait(timeout=10)
  assert process.returncode==0,process.stderr.read()
  assert not sock.exists()
  subprocess.run([binary,'restore-entry',str(entry)],env=env,check=True,timeout=5)
  assert entry.is_symlink()
  print('PASS broker installs entry/shell integration, preserves selections, reports ready and closes on EOF')
 finally:
  if process.poll() is None:process.kill();process.wait()
