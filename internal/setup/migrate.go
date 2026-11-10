package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// MigrateMagpieCredentials copies only the user's explicitly authorized model
// provider and subscription-login stores. Payloads travel via SSH stdin, never
// command arguments, diagnostics, model context or application events.
func MigrateMagpieCredentials(ctx context.Context, client *ssh.Client, home string) (int, error) {
	files := map[string]string{}
	for _, name := range []string{"providers.json", "logins.json", "plugin-auth.json"} {
		path := filepath.Join(home, ".config", "magpie", name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("cannot inspect Magpie credential store")
		}
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return 0, fmt.Errorf("invalid Magpie credential store")
		}
		file, err := os.Open(path)
		if err != nil {
			return 0, fmt.Errorf("cannot open Magpie credential store")
		}
		data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		file.Close()
		if err != nil || len(data) > 4<<20 || !json.Valid(data) {
			return 0, fmt.Errorf("invalid Magpie credential JSON")
		}
		if name == "plugin-auth.json" {
			var entries map[string]json.RawMessage
			if json.Unmarshal(data, &entries) != nil {
				return 0, fmt.Errorf("invalid plugin credential store")
			}
			selected := map[string]json.RawMessage{}
			for id, value := range entries {
				base := strings.SplitN(id, "#", 2)[0]
				if base == "devin" || base == "grok" {
					selected[id] = value
				}
			}
			if len(selected) == 0 {
				continue
			}
			data, _ = json.Marshal(selected)
		}
		files[name] = base64.StdEncoding.EncodeToString(data)
	}
	if len(files) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(files)
	if err != nil {
		return 0, fmt.Errorf("cannot encode credential migration")
	}
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(string(payload))
	// Receiver prints nothing, including errors containing credential fragments.
	session.Stdout = io.Discard
	session.Stderr = io.Discard
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	if err = session.Run("python3 -c " + shellQuote(migrateReceiver)); err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 4 {
			return 0, fmt.Errorf("subscription login conflict: local and remote both changed; remote credentials preserved")
		}
		return 0, fmt.Errorf("credential migration refused or interrupted; remote changes preserved")
	}
	return len(files), nil
}

// Baselines prevent a later local refresh overwriting remote token refreshes.
// Validate all inputs/conflicts before changing any credential file.
const migrateReceiver = `import base64,hashlib,json,os,pathlib,sys,tempfile,fcntl
root=pathlib.Path.home()/'.config'/'magpie'
root.mkdir(mode=0o700,parents=True,exist_ok=True)
os.umask(0o077)
lock=open(root/'.fennec-migration.lock','a')
fcntl.flock(lock,fcntl.LOCK_EX)
marker=root/'.fennec-migration.json'
old=json.loads(marker.read_text()) if marker.exists() else {}
incoming=json.load(sys.stdin)
if set(incoming)-{'providers.json','logins.json','plugin-auth.json'}: sys.exit(2)
planned=[]
for name,encoded in incoming.items():
 data=base64.b64decode(encoded,validate=True)
 if len(data)>4*1024*1024: sys.exit(2)
 parsed=json.loads(data)
 target=root/name
 if name=='logins.json':
  if not isinstance(parsed,list) or target.is_symlink(): sys.exit(3)
  remote=json.loads(target.read_text()) if target.exists() else []
  if not isinstance(remote,list): sys.exit(3)
  def identity(entry):
   return (entry.get('agent'),entry.get('id')) if entry.get('id') else None
  for entry in parsed:
   if not isinstance(entry,dict): sys.exit(2)
   if entry.get('home') or entry.get('hidden') or entry.get('own') or str(entry.get('agent','')).startswith('plugin:'): continue
   key=identity(entry)
   if key is None or not entry.get('auth'): continue
   # Existing remote identities win, including revoked/hidden records. Never
   # resurrect or downgrade a login while a remote gateway refreshes it.
   matches=[e for e in remote if identity(e)==key or (e.get('agent')==entry.get('agent') and e.get('user')==entry.get('user'))]
   baseline_key='login:'+hashlib.sha256(json.dumps(key).encode()).hexdigest()
   def auth_digest(value):
    return hashlib.sha256(json.dumps(value.get('auth'),sort_keys=True).encode()).hexdigest()
   incoming_digest=auth_digest(entry)
   if matches:
    if len(matches)!=1: sys.exit(4)
    existing=matches[0]
    if existing.get('hidden') or existing.get('home') or existing.get('own'): continue
    current_digest=auth_digest(existing)
    baseline=old.get(baseline_key)
    if current_digest==incoming_digest:
     old[baseline_key]=incoming_digest
     continue
    if baseline is None or incoming_digest==baseline: continue
    if current_digest!=baseline: sys.exit(4)
    existing['auth']=entry['auth']
    old[baseline_key]=incoming_digest
    continue
   remote.append(entry)
   old[baseline_key]=incoming_digest
  planned.append((target,json.dumps(remote).encode(),None))
  continue
 if name=='plugin-auth.json':
  if not isinstance(parsed,dict) or any(k.split('#')[0] not in ('devin','grok') for k in parsed): sys.exit(2)
  if target.is_symlink(): sys.exit(3)
  remote=json.loads(target.read_text()) if target.exists() else {}
  for key,value in parsed.items():
   entry_digest=hashlib.sha256(json.dumps(value,sort_keys=True).encode()).hexdigest()
   baseline=old.get('plugin:'+key)
   if key in remote:
    current=hashlib.sha256(json.dumps(remote[key],sort_keys=True).encode()).hexdigest()
    if current==entry_digest: old['plugin:'+key]=entry_digest; continue
    if entry_digest==baseline: continue
    if current!=baseline: sys.exit(3)
   remote[key]=value
   old['plugin:'+key]=entry_digest
  data=json.dumps(remote).encode()
  planned.append((target,data,None))
  continue
 digest=hashlib.sha256(data).hexdigest()
 if target.is_symlink(): sys.exit(3)
 if target.exists():
  current=hashlib.sha256(target.read_bytes()).hexdigest()
  if current==digest: old[name]=digest; continue
  # Unchanged local source must never roll back a remotely refreshed token.
  if digest==old.get(name): continue
  if current!=old.get(name): sys.exit(4 if name=='logins.json' else 3)
 planned.append((target,data,digest))
def atomic(path,data):
 fd,tmp=tempfile.mkstemp(prefix='.fennec-',dir=root)
 try:
  with os.fdopen(fd,'wb') as f: f.write(data);f.flush();os.fsync(f.fileno())
  os.replace(tmp,path)
 finally:
  if os.path.exists(tmp):os.unlink(tmp)
for target,data,digest in planned:
 atomic(target,data)
 if digest is not None: old[target.name]=digest
 atomic(marker,json.dumps(old).encode())
atomic(marker,json.dumps(old).encode())
`
