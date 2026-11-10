"""Real SSH direct-tcpip forwarding with pairing revocation on one connection."""
import json,os,pathlib,socket,struct,subprocess,tempfile,threading,time
binary=os.environ.get('FENNEC_TEST_BINARY','build/Fennec.app/Contents/MacOS/fennec-daemon')
with tempfile.TemporaryDirectory() as d:
 root=pathlib.Path(d)
 subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',d+'/key'],check=True)
 (root/'authorized_keys').write_bytes((root/'key.pub').read_bytes());(root/'authorized_keys').chmod(0o600)
 target=socket.socket();target.bind(('127.0.0.1',0));target.listen()
 def echo():
  for _ in range(2):
   conn,_=target.accept()
   with conn:
    data=conn.recv(100);conn.sendall(data)
 threading.Thread(target=echo,daemon=True).start()
 daemon=subprocess.Popen([binary,'connector','-state-dir',d],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
 tunnel=None
 try:
  port=json.loads(daemon.stdout.readline())['address'].rsplit(':',1)[1]
  approvals=[]
  def approve_forward():
   for line in daemon.stdout:
    event=json.loads(line)
    if event['type']=='approval':
     approvals.append(event['command'])
     daemon.stdin.write(json.dumps({'id':event['id'],'allow':True})+'\n');daemon.stdin.flush()
  threading.Thread(target=approve_forward,daemon=True).start()
  reserve=socket.socket();reserve.bind(('127.0.0.1',0));forward=reserve.getsockname()[1];reserve.close()
  reserve=socket.socket();reserve.bind(('127.0.0.1',0));socks=reserve.getsockname()[1];reserve.close()
  tunnel=subprocess.Popen(['ssh','-NT','-o','BatchMode=yes','-o','ExitOnForwardFailure=yes','-o','IdentitiesOnly=yes','-o','StrictHostKeyChecking=accept-new','-o','UserKnownHostsFile='+d+'/hosts','-i',d+'/key','-p',port,'-D',f'127.0.0.1:{socks}','-L',f'127.0.0.1:{forward}:127.0.0.1:{target.getsockname()[1]}','fennec@127.0.0.1'],stderr=subprocess.PIPE)
  for _ in range(100):
   try:conn=socket.create_connection(('127.0.0.1',forward),timeout=1);break
   except OSError:time.sleep(.02)
  with conn:
   conn.sendall(b'forward-ok');assert conn.recv(100)==b'forward-ok'
  def exact(sock,n):
   data=b''
   while len(data)<n:
    part=sock.recv(n-len(data))
    if not part: raise RuntimeError('unexpected EOF')
    data+=part
   return data
  with socket.create_connection(('127.0.0.1',socks),timeout=3) as proxy:
   proxy.sendall(b'\x05\x01\x00');assert exact(proxy,2)==b'\x05\x00'
   proxy.sendall(b'\x05\x01\x00\x01'+socket.inet_aton('127.0.0.1')+struct.pack('!H',target.getsockname()[1]))
   response=exact(proxy,4);assert response[:2]==b'\x05\x00',response
   if response[3]==1:exact(proxy,6)
   elif response[3]==4:exact(proxy,18)
   else:raise AssertionError(response)
   proxy.sendall(b'socks-ok');assert exact(proxy,8)==b'socks-ok'
  print('PASS SSH -D SOCKS5 negotiation and data flow')
  (root/'authorized_keys').write_text('')
  with socket.create_connection(('127.0.0.1',forward),timeout=2) as denied:
   try:denied.sendall(b'no');assert denied.recv(100)==b''
   except ConnectionResetError:pass
  assert len(approvals)==2,approvals
  assert all(x.startswith('TCP forwarding to ') for x in approvals),approvals
  print('PASS SSH -L/-D each request approved; revoked key cannot request another forward')
 finally:
  if tunnel:tunnel.terminate();tunnel.wait(timeout=5)
  daemon.stdin.close();daemon.wait(timeout=5);target.close()
