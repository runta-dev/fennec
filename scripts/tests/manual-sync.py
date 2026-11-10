"""Isolated real Mutagen test for Fennec's manual flush contract."""
import os,pathlib,tempfile,subprocess,json
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory(prefix='fennec-manual-sync-') as folder:
 root=pathlib.Path(folder);a=root/'local';b=root/'cloud';a.mkdir();b.mkdir()
 env={**os.environ,'MUTAGEN_DATA_DIRECTORY':str(root/'state')}
 def mutagen(*args):
  return subprocess.run(['mutagen',*args],env=env,text=True,capture_output=True,timeout=25,check=True)
 def flush():
  return subprocess.run([binary,'sync','flush','--state-dir',str(root/'state'),'fennec-test'],env=env,text=True,capture_output=True,timeout=30)
 try:
  (a/'file').write_text('baseline');mutagen('daemon','start')
  mutagen('sync','create','--name','fennec-test','--no-global-configuration','--mode','two-way-safe',str(a),str(b))
  result=flush();assert result.returncode==0,(result.stdout,result.stderr);assert json.loads(result.stdout)['ready']
  mutagen('sync','pause','fennec-test');(a/'file').write_text('local');(b/'file').write_text('cloud')
  mutagen('sync','resume','fennec-test')
  result=flush();assert result.returncode!=0;status=json.loads(result.stdout);assert not status['ready'] and status['session']['conflicts']
  assert (a/'file').read_text()=='local' and (b/'file').read_text()=='cloud'
  print('PASS manual flush success and conflict exit code; both versions preserved')
 finally:
  subprocess.run(['mutagen','sync','terminate','--all'],env=env,capture_output=True,timeout=20)
  subprocess.run(['mutagen','daemon','stop'],env=env,capture_output=True,timeout=20)
