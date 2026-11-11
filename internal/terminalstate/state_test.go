package terminalstate

import (
	"bytes"
	"errors"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"os"
	"reflect"
	"strings"
	"testing"
)

type checkingWriter struct {
	t     *testing.T
	input *os.File
	raw   *term.State
	bytes.Buffer
	fail bool
}

func (w *checkingWriter) Write(p []byte) (int, error) {
	current, err := term.GetState(int(w.input.Fd()))
	if err != nil || !reflect.DeepEqual(current, w.raw) {
		w.t.Fatal("echo restored before reporting disabled", err)
	}
	if w.fail {
		return 0, errors.New("output disconnected")
	}
	return w.Buffer.Write(p)
}
func TestRestoreDisablesReportingBeforeEchoAndDiscardsQueuedReports(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "output_failure"}[fail], func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			saved, err := term.MakeRaw(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := term.GetState(int(slave.Fd()))
			_, err = master.Write([]byte("\x1b[<35;29;36M"))
			if err != nil {
				t.Fatal(err)
			}
			poll := []unix.PollFd{{Fd: int32(slave.Fd()), Events: unix.POLLIN}}
			if n, e := unix.Poll(poll, 1000); e != nil || n == 0 {
				t.Fatal("input not queued", e)
			}
			output := &checkingWriter{t: t, input: slave, raw: raw, fail: fail}
			err = Restore(slave, output, saved)
			if (err != nil) != fail {
				t.Fatal(err)
			}
			restored, e := term.GetState(int(slave.Fd()))
			if e != nil || !reflect.DeepEqual(restored, saved) {
				t.Fatal("termios not restored", e)
			}
			if !fail {
				for _, mode := range []string{"9", "1000", "1001", "1002", "1003", "1004", "1005", "1006", "1015", "1016"} {
					if !strings.Contains(output.String(), "\x1b[?"+mode+"l") {
						t.Fatalf("mode %s left enabled", mode)
					}
				}
			}
			// Inspect the queue in raw mode, so canonical buffering cannot hide leftovers.
			again, e := term.MakeRaw(int(slave.Fd()))
			if e != nil {
				t.Fatal(e)
			}
			defer term.Restore(int(slave.Fd()), again)
			poll[0].Revents = 0
			if n, e := unix.Poll(poll, 0); e != nil || n != 0 {
				t.Fatal("stale mouse input survived", n, e)
			}
		})
	}
}
func (w *checkingWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
