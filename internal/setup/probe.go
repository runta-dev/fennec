package setup

import (
	"fmt"
	"strings"
)

// A catalog is advisory. Probe only a user-selected route, with one tiny
// request per model per reconcile; never send user/project content or tool access.
func modelProbeScript(model string) (string, error) {
	if !strings.Contains(model, "/") || len(model) > 512 || strings.ContainsAny(model, "\r\n\x00") {
		return "", fmt.Errorf("invalid model probe target")
	}
	return "python3 - " + shellQuote(model) + " <<'PROBE'\n" + `import json,sys,urllib.request,urllib.error,socket
body={'model':sys.argv[1],'messages':[{'role':'user','content':'Reply only OK.'}],'max_tokens':64,'stream':False}
request=urllib.request.Request('http://127.0.0.1:3425/v1/chat/completions',data=json.dumps(body).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer magpie'})
try:
 with urllib.request.urlopen(request,timeout=45) as response:
  data=json.loads(response.read(1024*1024))
  choices=data.get('choices',[])
  if not isinstance(choices,list) or not choices or data.get('error'):raise ValueError()
  message=choices[0].get('message',{})
  content=message.get('content')
  reasoning=message.get('reasoning_content')
  answered=isinstance(content,str) and bool(content.strip())
  reasoning_limited=choices[0].get('finish_reason')=='length' and isinstance(reasoning,str) and bool(reasoning.strip())
  if not answered and not reasoning_limited:raise ValueError()
except urllib.error.HTTPError as error:
 if error.code==429:sys.exit(75)
 if error.code==401:sys.exit(77)
 if error.code==403:sys.exit(78)
 if error.code>=500:sys.exit(76)
 sys.exit(1)
except (urllib.error.URLError,TimeoutError,socket.timeout):
 sys.exit(76)
except Exception:
 raise SystemExit('selected model did not pass inference verification')
PROBE
`, nil
}
