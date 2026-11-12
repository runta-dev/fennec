package localssh

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed assets/relay.py
var relaySource string

// StartRelay uses supported exec and local forwarding channels rather than
// requesting gateway-level remote forwarding. Both guest listeners are loopback.
func StartRelay(ctx context.Context, client *ssh.Client, device string) (int, int, error) {
	if _, err := DeviceAlias("machine", device); err != nil {
		return 0, 0, err
	}
	script := relaySetupScript(device)
	session, err := client.NewSession()
	if err != nil {
		return 0, 0, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	output, err := session.Output("sh -s")
	if err != nil {
		return 0, 0, err
	}
	var state struct {
		Bridge int `json:"bridge_port"`
		Agent  int `json:"agent_port"`
	}
	if json.Unmarshal(output, &state) != nil || state.Bridge < 1 || state.Bridge > 65535 || state.Agent < 1 || state.Agent > 65535 {
		return 0, 0, fmt.Errorf("invalid relay state")
	}
	return state.Bridge, state.Agent, nil
}

func relaySetupScript(device string) string {
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(relaySource)))
	return `set -eu
umask 077
base="$HOME/.local/fennec/relay-` + device + `"
mkdir -p "$base"
exec 9>"$base/setup.lock"
flock -w 20 9
printf '%s' '` + base64.StdEncoding.EncodeToString([]byte(relaySource)) + `' | base64 -d > "$base/relay.py.next"
mv "$base/relay.py.next" "$base/relay.py"
# Reuse a live device-specific service rather than accumulating listeners.
if [ -f "$base/state.json" ]; then
 python3 - "$base/state.json" '` + revision + `' <<'CHECK' && exit 0
import json,sys,os,socket,pathlib,signal,time
try:
 path=pathlib.Path(sys.argv[1]);s=json.loads(path.read_text());pid=int(s['pid'])
 if pid<=1:raise ValueError('invalid pid')
 proc=pathlib.Path('/proc')/str(pid)
 argv=(proc/'cmdline').read_bytes().split(b'\0')
 expected=str(path.parent/'relay.py').encode()
 if expected not in argv or b'--state' not in argv or str(path).encode() not in argv:
  raise ValueError('relay process identity mismatch')
 if s.get('revision')==sys.argv[2]:
  with socket.create_connection(('127.0.0.1',s['bridge_port']),2):pass
  print(json.dumps(s));sys.exit(0)
 # Stop only the verified device relay, never Herdr or its agent sessions.
 os.kill(pid,signal.SIGTERM)
 for _ in range(40):
  if not proc.exists() or (proc/'stat').read_text().split(') ',1)[1].startswith('Z'):break
  time.sleep(.05)
 else:raise RuntimeError('old relay did not stop')
except (FileNotFoundError,ProcessLookupError):pass
except Exception as error:
 print('relay reconciliation failed: '+type(error).__name__,file=sys.stderr)
 sys.exit(2)
sys.exit(1)
CHECK
 result=$?
 [ "$result" -eq 1 ] || exit "$result"
fi
rm -f "$base/state.json"
setsid nohup python3 "$base/relay.py" --state "$base/state.json" </dev/null >"$base/log" 2>&1 9>&- &
i=0
while [ "$i" -lt 20 ]; do
 if [ -s "$base/state.json" ]; then cat "$base/state.json"; exit 0; fi
 i=$((i+1)); sleep 1
done
exit 70
`
}

// Relay capacity: parkedBridges idle device sockets wait for agents at all
// times, and a claimed socket is replaced immediately rather than when its
// connection ends. Long-lived connections (such as desktop MCP servers) can
// therefore never starve new commands; maxRelayConnections bounds the total.
const (
	parkedBridges       = 8
	maxRelayConnections = 64
)

func serveRelay(ctx context.Context, client *ssh.Client, port int, local string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	active := make(chan struct{}, maxRelayConnections)
	forward := func(remote net.Conn) {
		defer workers.Done()
		defer func() { <-active }()
		defer remote.Close()
		target, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", local)
		if err != nil {
			return
		}
		stopLocal := context.AfterFunc(ctx, func() { target.Close(); remote.Close() })
		defer stopLocal()
		done := make(chan struct{})
		go func() { io.Copy(remote, target); remote.Close(); close(done) }()
		io.Copy(target, remote)
		target.Close()
		<-done
	}
	for i := 0; i < parkedBridges; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				remote, err := client.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					cancel()
					return
				}
				stop := context.AfterFunc(ctx, func() { remote.Close() })
				var marker [1]byte
				_, err = io.ReadFull(remote, marker[:])
				stop()
				if err != nil {
					remote.Close()
					cancel()
					return
				}
				if marker[0] != 'F' {
					remote.Close()
					continue
				}
				select {
				case active <- struct{}{}:
					workers.Add(1)
					go forward(remote)
				default:
					remote.Close() // over capacity: refuse rather than queue
				}
			}
		}()
	}
	<-ctx.Done()
	client.Close()
	workers.Wait()
	return fmt.Errorf("device relay disconnected")
}
