package setup

// RestoreCodexIdentityScript delegates credential handling to Magpie. It only
// restores a single enabled migrated identity when the guest has no auth file.
// Multiple candidates or existing identities are preserved, never guessed over.
const RestoreCodexIdentityScript = `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/fennec/agents/bin:$HOME/.local/bin:$PATH"
python3 - <<'RESTORE'
import json,pathlib,subprocess
home=pathlib.Path.home()
auth=home/'.codex'/'auth.json'
if auth.exists(): raise SystemExit(0)
store=home/'.config'/'magpie'/'logins.json'
if not store.exists(): raise SystemExit(0)
entries=json.loads(store.read_text())
candidates=[e for e in entries if e.get('agent')=='codex' and e.get('on') is True and not e.get('hidden') and not e.get('home') and e.get('auth') and e.get('user')]
if len(candidates)!=1: raise SystemExit(0)
# CLI output may contain account metadata; never forward it to setup logs.
result=subprocess.run(['magpie','accounts','switch','codex',candidates[0]['user']],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=30)
if result.returncode: raise SystemExit('model account restoration failed')
if not auth.exists(): raise SystemExit('model account restoration did not create guest identity')
RESTORE
`
