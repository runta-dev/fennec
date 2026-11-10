import tempfile,subprocess,json,os
with tempfile.TemporaryDirectory(prefix='fennec-ssh-') as d:
 subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',d+'/client'],check=True)
 open(d+'/authorized_keys','w').write(open(d+'/client.pub').read())
 daemon=subprocess.Popen(['build/Fennec.app/Contents/MacOS/fennec-daemon','connector','-require-approval','-state-dir',d],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
 try:
  event=json.loads(daemon.stdout.readline());port=event['address'].split(':')[-1]
  for allow in [False,True]:
   client=subprocess.Popen(['ssh','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+d+'/known_hosts','-o','IdentitiesOnly=yes','-i',d+'/client','-p',port,'machine@127.0.0.1','printf fennec-approved; exit 7'],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
   while True:
    event=json.loads(daemon.stdout.readline())
    if event['type']=='approval':break
   daemon.stdin.write(json.dumps({'id':event['id'],'allow':allow})+'\n');daemon.stdin.flush()
   out,err=client.communicate(timeout=15)
   assert client.returncode==(7 if allow else 126),(client.returncode,err)
   assert out==('fennec-approved' if allow else ''),out
   print('PASS', 'approved command stdout/exit' if allow else 'denied command not executed')
 finally:
  daemon.stdin.close();daemon.wait(timeout=5)
 print('PASS parent EOF shutdown')
