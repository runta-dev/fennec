"""Real SFTP round trip in a temporary directory, with explicit session approval."""
import json,os,pathlib,subprocess,tempfile,threading,queue
binary=os.environ.get('FENNEC_TEST_BINARY','build/Fennec.app/Contents/MacOS/fennec-daemon')
with tempfile.TemporaryDirectory(prefix='fennec-sftp-') as d:
 root=pathlib.Path(d)
 subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',d+'/key'],check=True)
 (root/'authorized_keys').write_bytes((root/'key.pub').read_bytes());(root/'authorized_keys').chmod(0o600)
 (root/'source').write_bytes(b'fennec\x00sftp\n')
 daemon=subprocess.Popen([binary,'connector','-state-dir',d],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
 events=queue.Queue()
 def read():
  for line in daemon.stdout:events.put(json.loads(line))
 threading.Thread(target=read,daemon=True).start()
 try:
  port=events.get(timeout=5)['address'].rsplit(':',1)[1]
  batch=f'put {d}/source {d}/remote\nget {d}/remote {d}/received\nrm {d}/remote\n'
  client=subprocess.Popen(['sftp','-b','-','-P',port,'-i',d+'/key','-o','IdentitiesOnly=yes','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+d+'/hosts','fennec@127.0.0.1'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
  while True:
   event=events.get(timeout=8)
   if event['type']=='approval':break
  assert event['command'].startswith('SFTP:')
  daemon.stdin.write(json.dumps({'id':event['id'],'allow':True})+'\n');daemon.stdin.flush()
  out,err=client.communicate(batch,timeout=15)
  assert client.returncode==0,err
  assert (root/'received').read_bytes()==(root/'source').read_bytes()
  assert not (root/'remote').exists()
  print('PASS approved SFTP binary upload/download/delete')
 finally:
  daemon.stdin.close();daemon.wait(timeout=5)
