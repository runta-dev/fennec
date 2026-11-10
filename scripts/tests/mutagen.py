import os,pathlib,tempfile,subprocess,json
with tempfile.TemporaryDirectory(prefix='fennec-mutagen-') as folder:
 root=pathlib.Path(folder);a=root/'mac';b=root/'cloud';a.mkdir();b.mkdir()
 env={**os.environ,'MUTAGEN_DATA_DIRECTORY':str(root/'state')}
 def run(*args):
  r=subprocess.run(['mutagen',*args],env=env,text=True,capture_output=True,timeout=25)
  if r.returncode:raise RuntimeError(r.stderr)
  return r.stdout
 try:
  run('daemon','start');(a/'base.txt').write_text('baseline')
  run('sync','create','--name','fennec-check','--no-global-configuration','--mode','two-way-safe','--ignore-vcs','--symlink-mode','ignore',str(a),str(b))
  run('sync','flush','fennec-check');assert (b/'base.txt').read_text()=='baseline'
  run('sync','pause','fennec-check')
  (a/'base.txt').write_text('local edit');(b/'base.txt').write_text('cloud edit');(b/'offline.txt').write_text('cloud continued')
  run('sync','resume','fennec-check');run('sync','flush','fennec-check')
  assert (a/'base.txt').read_text()=='local edit' and (b/'base.txt').read_text()=='cloud edit'
  assert (a/'offline.txt').read_text()=='cloud continued'
  status=run('sync','list','fennec-check','--template','{{json .}}')
  pathlib.Path('/tmp/fennec-mutagen-status.json').write_text(status)
  print('PASS initial sync, paused-cloud work, reconnect propagation, both conflict versions preserved')
 finally:
  subprocess.run(['mutagen','sync','terminate','--all'],env=env,capture_output=True,timeout=20)
  subprocess.run(['mutagen','daemon','stop'],env=env,capture_output=True,timeout=20)
