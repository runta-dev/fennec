"""Current-directory manual sync against a real isolated Mutagen daemon."""
import json,os,pathlib,subprocess,tempfile
binary=os.environ['FENNEC_TEST_BINARY']
with tempfile.TemporaryDirectory(prefix='fennec-project-sync-',dir='/tmp') as folder:
 root=pathlib.Path(folder);home=root/'home';a=home/'project';b=root/'cloud';sub=a/'src'
 sub.mkdir(parents=True);b.mkdir();state=root/'state'
 env={**os.environ,'HOME':str(home),'MUTAGEN_DATA_DIRECTORY':str(state)}
 def mutagen(*args):return subprocess.run(['mutagen',*args],env=env,check=True,capture_output=True,text=True,timeout=25)
 try:
  (sub/'file').write_text('from local')
  mutagen('daemon','start');mutagen('sync','create','--name','fennec-project-test','--no-global-configuration','--mode','two-way-safe',str(a),str(b))
  registry=home/'.config/fennec/projects';registry.mkdir(parents=True)
  registration={'root':str(a),'session':'fennec-project-test','data_directory':str(state),'ssh_directory':''}
  (registry/'one.json').write_text(json.dumps(registration))
  result=subprocess.run([binary,'sync','flush'],cwd=sub,env=env,capture_output=True,text=True,timeout=30)
  assert result.returncode==0,(result.stdout,result.stderr)
  assert json.loads(result.stdout)['ready'] and (b/'src/file').read_text()=='from local'
  registration['session']='fennec-other-account';(registry/'two.json').write_text(json.dumps(registration))
  result=subprocess.run([binary,'sync','flush'],cwd=sub,env=env,capture_output=True,text=True,timeout=10)
  assert result.returncode!=0 and 'found 2' in result.stderr,result.stderr
  print('PASS cwd-based flush transfers files; ambiguous account registration fails closed')
 finally:
  subprocess.run(['mutagen','sync','terminate','--all'],env=env,capture_output=True,timeout=20)
  subprocess.run(['mutagen','daemon','stop'],env=env,capture_output=True,timeout=20)
