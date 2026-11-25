package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

// browser drives headless Chrome over --remote-debugging-pipe, which needs no
// websocket library: messages are NUL-terminated JSON on fds 3 and 4.
type browser struct {
	cmd     *exec.Cmd
	w       io.WriteCloser
	r       *bufio.Reader
	id      int
	session string
	tmp     string
}

func chromePath() string {
	if p := os.Getenv("CHROME"); p != "" {
		return p
	}
	if runtime.GOOS == "darwin" {
		return "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	for _, p := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	return "google-chrome"
}

func launchBrowser(width, height int) (*browser, error) {
	tmp, err := os.MkdirTemp("", "fennec-trailer-chrome-")
	if err != nil {
		return nil, err
	}
	toChrome, ourW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	ourR, fromChrome, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(chromePath(),
		"--headless=new",
		"--remote-debugging-pipe",
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-scrollbars",
		"--mute-audio",
		"--force-device-scale-factor=1",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--user-data-dir="+tmp,
		"about:blank",
	)
	if os.Geteuid() == 0 {
		cmd.Args = append(cmd.Args[:1], append([]string{"--no-sandbox"}, cmd.Args[1:]...)...)
	}
	cmd.ExtraFiles = []*os.File{toChrome, fromChrome}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chrome: %w", err)
	}
	toChrome.Close()
	fromChrome.Close()
	b := &browser{cmd: cmd, w: ourW, r: bufio.NewReaderSize(ourR, 1<<20), tmp: tmp}

	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := b.call("Target.getTargets", nil, &targets); err != nil {
		b.close()
		return nil, err
	}
	target := ""
	for _, t := range targets.TargetInfos {
		if t.Type == "page" {
			target = t.TargetID
			break
		}
	}
	if target == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := b.call("Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
			b.close()
			return nil, err
		}
		target = created.TargetID
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := b.call("Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, &att); err != nil {
		b.close()
		return nil, err
	}
	b.session = att.SessionID
	return b, nil
}

func (b *browser) call(method string, params any, result any) error {
	b.id++
	msg := map[string]any{"id": b.id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if b.session != "" && method[:7] != "Target." {
		msg["sessionId"] = b.session
	}
	data, _ := json.Marshal(msg)
	if _, err := b.w.Write(append(data, 0)); err != nil {
		return err
	}
	for {
		raw, err := b.r.ReadBytes(0)
		if err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw[:len(raw)-1], &resp); err != nil {
			return err
		}
		if resp.ID != b.id {
			continue // event or stale reply
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: %s", method, resp.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// eval runs a JavaScript expression (awaiting promises) and decodes its value.
func (b *browser) eval(expr string, out any) error {
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := b.call("Runtime.evaluate", map[string]any{
		"expression":    expr,
		"awaitPromise":  true,
		"returnByValue": true,
	}, &res); err != nil {
		return err
	}
	if e := res.ExceptionDetails; e != nil {
		if e.Exception != nil {
			return fmt.Errorf("js: %s", e.Exception.Description)
		}
		return fmt.Errorf("js: %s", e.Text)
	}
	if out != nil && len(res.Result.Value) > 0 {
		return json.Unmarshal(res.Result.Value, out)
	}
	return nil
}

func (b *browser) close() {
	b.w.Close()
	if b.cmd.Process != nil {
		b.cmd.Process.Kill()
		b.cmd.Wait()
	}
	os.RemoveAll(b.tmp)
}
