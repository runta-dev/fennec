package runtime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var sessionIDPattern = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)

// LaunchScript delegates terminal lifetime to Herdr Core; only idempotency metadata is local.
// The caller must authenticate runtime ownership and validate model readiness.
func LaunchScript(id, agent, cwd string) (string, error) {
	return LaunchScriptWithArgs(id, agent, cwd, nil)
}

func LaunchScriptWithArgs(id, agent, cwd string, arguments []string) (string, error) {
	return LaunchScriptWithVersion(id, agent, cwd, arguments, "")
}

func LaunchScriptWithVersion(id, agent, cwd string, arguments []string, version string) (string, error) {
	return launchScript(id, agent, cwd, arguments, version, false)
}

func CloudLaunchScript(id, agent, localDirectory, device string, arguments []string) (string, error) {
	return cloudLaunchScript(id, agent, localDirectory, device, arguments, false)
}

func CloudLaunchAttachScript(id, agent, localDirectory, device string, arguments []string) (string, error) {
	return cloudLaunchScript(id, agent, localDirectory, device, arguments, true)
}

func cloudLaunchScript(id, agent, localDirectory, device string, arguments []string, attach bool) (string, error) {
	request := LocalLaunch{ID: id, Agent: agent, LocalDirectory: localDirectory, Device: device, Arguments: arguments}
	if err := request.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	if attach {
		return "exec \"$HOME/.local/fennec/bin/fennec-terminal\" start " + base64.StdEncoding.EncodeToString(payload) + "\n", nil
	}
	quoted := "'" + strings.ReplaceAll(string(payload), "'", "'\"'\"'") + "'"
	return "printf '%s' " + quoted + " | \"$HOME/.local/fennec/bin/fennec-terminal\" launch\n", nil
}

func launchScript(id, agent, cwd string, arguments []string, version string, attach bool) (string, error) {
	if version != "" && !regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`).MatchString(version) {
		return "", fmt.Errorf("invalid agent version")
	}
	// The Runta runtime is the isolation boundary. Apply defaults only to
	// cloud invocations; local connector authorization remains independent.
	// Explicit user permission options can still select a stricter mode.
	commands := map[string][]string{
		"claude":   {"claude", "--permission-mode", "bypassPermissions"},
		"codex":    {"codex", "--no-daemon", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`},
		"pi":       {"pi"}, // Pi's built-in tools do not have a sandbox/approval gate.
		"opencode": {"opencode"},
		// Never Ask: Kimi decides tool calls itself; plans still wait.
		"kimi": {"kimi", "--auto"},
	}
	command, ok := commands[agent]
	if !ok || !sessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid agent/session identity")
	}
	if agent == "opencode" && strings.HasPrefix(version, "2.") {
		command = append(command, "--auto")
	}
	if !filepath.IsAbs(cwd) || strings.ContainsAny(cwd, "\x00\r\n") {
		return "", fmt.Errorf("absolute cloud working directory required")
	}
	size := 0
	if len(arguments) > 256 {
		return "", fmt.Errorf("too many agent arguments")
	}
	for _, arg := range arguments {
		size += len(arg)
		if strings.ContainsRune(arg, 0) || size > 32768 {
			return "", fmt.Errorf("invalid or oversized agent arguments")
		}
	}
	command = append(command, arguments...)
	request := map[string]any{"id": id, "agent": agent, "cwd": cwd, "command": command, "version": version}

	if attach {
		request["attach"] = true
	}
	payload, _ := json.Marshal(request)
	script := `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
python3 - '` + base64.StdEncoding.EncodeToString(payload) + `' <<'LAUNCH'
import base64,json,pathlib,subprocess,sys,fcntl,os,time
timings={}
phase=time.monotonic()
request=json.loads(base64.b64decode(sys.argv[1]))
def output_result(result):
 if not request.get('attach'):
  print(json.dumps(result));return
 result['id']=request['id']
 result['agent']=request['agent']
 fcntl.flock(lock,fcntl.LOCK_UN)
 print('\x1eFENNEC_RECEIPT '+json.dumps(result),flush=True)
 hc=str(pathlib.Path.home()/'.local/bin/herdr-core')
 if result.get('state')=='finished':
  subprocess.run([hc,'read',result['address'],'--lines','0'])
  raise SystemExit(result.get('exit_code',1))
 terminal=os.open('/dev/tty',os.O_RDWR)
 os.dup2(terminal,0)
 os.close(terminal)
 attached=subprocess.run([hc,'attach',result['address']])
 if attached.returncode==11:
  latest=json.loads(subprocess.check_output([hc,'get',result['address']],text=True,timeout=15))
  if latest.get('labels',{}).get('fennec.session')!=request['id']:raise SystemExit('terminal identity changed during attach')
  if latest.get('state')=='finished':
   subprocess.run([hc,'read',result['address'],'--lines','0'])
   raise SystemExit(latest.get('exit_code',1))
 raise SystemExit(attached.returncode if attached.returncode>=0 else 128-attached.returncode)
os.umask(0o077)
root=pathlib.Path.home()/'.local/fennec/launches'
root.mkdir(parents=True,exist_ok=True)
with open(root/(request['id']+'.lock'),'a') as lock:
 fcntl.flock(lock,fcntl.LOCK_EX)
 marker=root/(request['id']+'.json')
 if marker.exists() and json.loads(marker.read_text())!=request:raise SystemExit('session identity belongs to another launch')
 result=subprocess.run(['herdr-core','ls','--all','--json','--label','fennec.session='+request['id']],capture_output=True,text=True,check=True)
 terminals=json.loads(result.stdout).get('terminals',[])
 timings['session_lookup']=(time.monotonic()-phase)*1000
 phase=time.monotonic()
 if len(terminals)>1:raise SystemExit('duplicate session identity')
 if terminals:
  output_result(terminals[0]);raise SystemExit(0)
 if marker.exists():raise SystemExit('launch outcome unknown; inspect before retrying')
 if not pathlib.Path(request['cwd']).is_dir():raise SystemExit('cloud working directory does not exist')
 command=list(request['command'])
 # OpenCode v1 uses a permission configuration environment override. v2
 # uses --auto and continues to honor explicitly denied permissions.
 if request['agent']=='opencode':
  version=request['version']
  if not version:
   import re
   probe=subprocess.run(['opencode','--version'],capture_output=True,text=True,check=True,timeout=10)
   match=re.search(r'\b(\d+)\.',probe.stdout)
   if not match:raise SystemExit('unable to determine OpenCode permission mode')
   version=match.group(1)+'.'
  if version.startswith('2.'):
   if not request['version']:command.insert(1,'--auto')
  elif version.startswith('1.'):
   os.environ['OPENCODE_PERMISSION']='{"*":"allow"}'
  else:raise SystemExit('unsupported OpenCode permission mode')
 if request['version']:
  import re,shutil,time
  locks=pathlib.Path.home()/'.local/fennec/install-locks'
  locks.mkdir(parents=True,exist_ok=True)
  install_lock=open(locks/(request['agent']+'.lock'),'a')
  deadline=time.monotonic()+180
  while True:
   try:
    fcntl.flock(install_lock,fcntl.LOCK_SH|fcntl.LOCK_NB)
    break
   except BlockingIOError:
    if time.monotonic()>=deadline:raise SystemExit('agent installation is busy; retry launch')
    time.sleep(0.1)
  executable=shutil.which(request['agent'])
  if not executable:raise SystemExit('cloud agent executable missing')
  command[0]=str(pathlib.Path(executable).resolve(strict=True))
  check=subprocess.run([command[0],'--version'],capture_output=True,text=True,timeout=10)
  match=re.search(r'(?:^|\s)v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)',check.stdout+' '+check.stderr)
  if check.returncode or not match or match.group(1)!=request['version']:raise SystemExit('cloud agent version differs from local executable; reconcile required')
 timings['launch_validation']=(time.monotonic()-phase)*1000
 phase=time.monotonic()
 import shutil
 if not shutil.which(request['agent']):raise SystemExit('cloud agent executable missing')
 with open(marker,'x') as f:json.dump(request,f)
 # Herdr is a persistent server: pass the cloud sandbox declaration in the
 # child command, not only in this SSH client's environment. Never set it on
 # the user's local Claude entrypoint or for other agents.
 if request['agent']=='claude':command=['env','IS_SANDBOX=1']+command
 # Fennec made the session directory: Kimi Code trusts it up front.
 if request['agent']=='kimi':command=['env','` + kimiTrustWorkspace + `']+command
 args=['herdr-core','run','--bg','--no-parent','--outlive-parent','--json','--cwd',request['cwd'],'--label','fennec.session='+request['id'],'--label','fennec.agent='+request['agent'],'--']+command
 launched=subprocess.run(args,capture_output=True,text=True,timeout=80)
 if launched.returncode:raise SystemExit('Herdr launch failed; inspect before retrying')
 result=json.loads(launched.stdout)
 timings['herdr_spawn']=(time.monotonic()-phase)*1000
 result['fennec_profile_ms']=timings
 output_result(result)
LAUNCH
`
	return script, nil
}

// kimiTrustWorkspace trusts the session's working directory, which Fennec
// creates, so Kimi Code does not stop at its "Trust this folder?" prompt.
const kimiTrustWorkspace = "KIMI_CODE_TRUST_WORKSPACE=1"
