package launcher

import (
	"encoding/base64"
	"encoding/json"
)

func resumeHistoryCommand(address, sessionID string) string {
	payload, _ := json.Marshal(map[string]string{"address": address, "session": sessionID})
	program := `import base64,json,os,pathlib,re,sqlite3,subprocess,sys,fcntl,tempfile
request=json.loads(base64.b64decode(sys.argv[1]))
home=pathlib.Path.home()
hc=str(home/'.local/bin/herdr-core')
def get(address):
 result=subprocess.run([hc,'get',address],capture_output=True,text=True,check=True,timeout=15)
 value=json.loads(result.stdout)
 if value.get('labels',{}).get('fennec.session')!=request['session']:raise SystemExit('session identity mismatch')
 return value
terminal=get(request['address'])
if terminal.get('state')=='finished':
 agent=terminal.get('labels',{}).get('fennec.agent') or terminal.get('agent','').removesuffix('.js')
 if agent!='codex':raise SystemExit('This agent has exited. Start a new session.')
 directory=terminal.get('cwd','')
 if not pathlib.Path(directory).is_absolute() or not pathlib.Path(directory).is_dir():raise SystemExit('Session workspace is unavailable.')
 os.umask(0o077)
 locks=home/'.local/fennec/resume-locks'
 locks.mkdir(parents=True,exist_ok=True)
 with (locks/(request['session']+'.lock')).open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  listing=json.loads(subprocess.check_output([hc,'ls','--all','--json','--label','fennec.session='+request['session']],text=True,timeout=15))
  live=[item for item in listing.get('terminals',[]) if item.get('state')=='live']
  if len(live)>1:raise SystemExit('Multiple live terminals match this session. Inspect before retrying.')
  marker=locks/(request['session']+'.json')
  if live:
   terminal=get(live[0]['address'])
  else:
   if marker.exists() and json.loads(marker.read_text()).get('state')=='pending':raise SystemExit('Previous resume outcome is unknown. Inspect before retrying.')
   conversations=set()
   for database in (home/'.codex').glob('state_*.sqlite'):
    connection=sqlite3.connect(database.as_uri()+'?mode=ro',uri=True,timeout=2)
    try:
     columns={row[1] for row in connection.execute('pragma table_info(threads)')}
     if {'id','cwd','archived'}<=columns:
      conversations.update(row[0] for row in connection.execute('select id from threads where cwd=? and archived=0',(directory,)))
    finally:connection.close()
   if len(conversations)!=1:raise SystemExit('Could not identify one saved conversation for this session.')
   conversation=next(iter(conversations))
   if not re.fullmatch(r'[a-f0-9-]{36}',conversation):raise SystemExit('Invalid saved conversation identity.')
   def save(state):
    descriptor,temporary=tempfile.mkstemp(dir=locks)
    try:
     with os.fdopen(descriptor,'w') as file:json.dump({'state':state},file);file.flush();os.fsync(file.fileno())
     os.replace(temporary,marker)
    finally:
     if os.path.exists(temporary):os.unlink(temporary)
   save('pending')
   environment=dict(os.environ,PATH=str(home/'.local/fennec/node/bin')+':'+str(home/'.local/fennec/agents/bin')+':'+str(home/'.local/bin')+':'+os.environ.get('PATH',''))
   command=[hc,'run','--bg','--no-parent','--outlive-parent','--json','--cwd',directory,'--label','fennec.session='+request['session'],'--label','fennec.agent=codex','--','codex','--no-daemon','-c','sandbox_mode="danger-full-access"','-c','approval_policy="never"','resume',conversation]
   result=subprocess.run(command,capture_output=True,text=True,env=environment,timeout=80)
   if result.returncode:raise SystemExit('Resume not confirmed. Inspect before retrying.')
   terminal=get(json.loads(result.stdout)['address'])
   save('complete')
attached=subprocess.run([str(home/'.local/fennec/bin/fennec-terminal'),'attach',terminal['address'],request['session']])
if attached.returncode in (0,11):
 terminal=get(terminal['address'])
 print('\nTo resume: fen resume '+request['session'],flush=True)
 if terminal.get('state')=='finished':
  code=terminal.get('exit_code')
  if isinstance(code,int) and not isinstance(code,bool) and 0<=code<=255:raise SystemExit(code)
raise SystemExit(attached.returncode if attached.returncode>=0 else 128-attached.returncode)
`
	encoded := base64.StdEncoding.EncodeToString([]byte(program))
	return "python3 -c \"import base64;exec(base64.b64decode('" + encoded + "'))\" '" + base64.StdEncoding.EncodeToString(payload) + "'"
}
