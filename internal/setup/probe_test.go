package setup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferenceProbeClassifiesHTTPFailuresWithoutLeakingBody(t *testing.T) {
	for _, scenario := range []struct{ status, exit int }{{429, 75}, {503, 76}, {401, 77}, {403, 78}, {404, 1}} {
		directory := t.TempDir()
		hook := `import urllib.request,urllib.error,os,io
def urlopen(*args,**kwargs):
 raise urllib.error.HTTPError('http://localhost',int(os.environ['PROBE_STATUS']),'private response',{},io.BytesIO(b'secret'))
urllib.request.urlopen=urlopen
`
		if err := os.WriteFile(filepath.Join(directory, "sitecustomize.py"), []byte(hook), 0600); err != nil {
			t.Fatal(err)
		}
		script, err := modelProbeScript("test/model")
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("sh", "-s")
		command.Stdin = strings.NewReader(script)
		command.Env = append(os.Environ(), "PYTHONPATH="+directory, fmt.Sprintf("PROBE_STATUS=%d", scenario.status))
		output, err := command.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != scenario.exit || len(output) != 0 {
			t.Fatalf("status %d: exit %v output %q", scenario.status, err, output)
		}
	}
}

func TestInferenceProbeValidatesResponseAndRedactsErrors(t *testing.T) {
	for _, test := range []struct {
		name, body string
		success    bool
	}{
		{"valid", `{"choices":[{"message":{"content":"OK"}}]}`, true},
		{"empty", `{"choices":[]}`, false},
		{"blank", `{"choices":[{"message":{"content":" "}}]}`, false},
		{"reasoning-only inference", `{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":"Reasoning output"}}]}`, true},
		{"empty truncated response", `{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":""}}]}`, false},
		{"reasoning token budget", `{"choices":[{"message":{"content":"OK","reasoning_content":"Thinking"}}]}`, true},
		{"error", `{"error":{"message":"sensitive-server-detail"}}`, false},
		{"malformed", `not-json-sensitive-server-detail`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			// Python's supported sitecustomize hook replaces only the network boundary;
			// the actual generated request/response validation script runs unchanged.
			hook := `import urllib.request,os,json
class Response:
 def __enter__(self):return self
 def __exit__(self,*args):pass
 def read(self,limit):return os.environ['PROBE_RESPONSE'].encode()[:limit]
def urlopen(request,timeout):
 body=json.loads(request.data)
 assert body['model']=='test/model'
 assert body['max_tokens']==64 and body['stream'] is False
 assert body['messages']==[{'role':'user','content':'Reply only OK.'}]
 assert timeout==45
 return Response()
urllib.request.urlopen=urlopen
`
			if err := os.WriteFile(filepath.Join(dir, "sitecustomize.py"), []byte(hook), 0600); err != nil {
				t.Fatal(err)
			}
			script, err := modelProbeScript("test/model")
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-s")
			cmd.Stdin = strings.NewReader(script)
			cmd.Env = append(os.Environ(), "PYTHONPATH="+dir, "PROBE_RESPONSE="+test.body)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.success {
				t.Fatalf("success=%v err=%v output=%s", test.success, err, output)
			}
			if strings.Contains(string(output), "sensitive-server-detail") {
				t.Fatal("leaked response body")
			}
		})
	}
}
