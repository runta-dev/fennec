package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/byoc"
	"fennec/internal/byoc/byoctest"
	"fennec/internal/control"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newJobControl(t *testing.T) (*byocControl, *account.Manager, string) {
	t.Helper()
	home := t.TempDir()
	manager := account.New(context.Background(), &tokenCredentials{}, "http://127.0.0.1:1")
	c := newBYOCControl(home, manager)
	c.restore = nil
	return c, manager, home
}

func startJob(t *testing.T, c *byocControl, request byocStartRequest) error {
	t.Helper()
	raw, _ := json.Marshal(request)
	_, err := c.start(context.Background(), raw)
	return err
}

// waitJob polls status the way the menu bar does until the job finishes.
func waitJob(t *testing.T, c *byocControl) byocStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		result, err := c.status(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		status := result.(byocStatus)
		if status.Job != nil && (status.Job.Stage == "done" || status.Job.Stage == "failed") {
			return status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return byocStatus{}
}

func address(computer *byoctest.Computer) string {
	e := computer.Endpoint()
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

func TestPairJobFromTheMenuBar(t *testing.T) {
	c, manager, home := newJobControl(t)
	computer := byoctest.New(t)
	computer.Authorize(t, home)
	if err := startJob(t, c, byocStartRequest{Mode: "pair", Address: address(computer), Fingerprint: computer.Fingerprint()}); err != nil {
		t.Fatal(err)
	}
	status := waitJob(t, c)
	if status.Job.Stage != "done" || !status.Paired || status.MachineID != byoctest.Machine {
		t.Fatalf("unexpected status %+v job %+v", status, *status.Job)
	}
	if manager.Snapshot().Status != account.StatusPaired {
		t.Fatal("pairing did not switch the principal")
	}
}

func TestPairJobReportsWhyItFailed(t *testing.T) {
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := closed.Addr().String()
	closed.Close()
	for name, setup := range map[string]func(t *testing.T, home string) byocStartRequest{
		byocErrFingerprint: func(t *testing.T, home string) byocStartRequest {
			computer, other := byoctest.New(t), byoctest.New(t)
			computer.Authorize(t, home)
			return byocStartRequest{Mode: "pair", Address: address(computer), Fingerprint: other.Fingerprint()}
		},
		byocErrNotAuthorized: func(t *testing.T, _ string) byocStartRequest {
			computer := byoctest.New(t)
			return byocStartRequest{Mode: "pair", Address: address(computer), Fingerprint: computer.Fingerprint()}
		},
		byocErrNotFennec: func(t *testing.T, home string) byocStartRequest {
			computer := byoctest.New(t)
			computer.Authorize(t, home)
			computer.SetIdentity("")
			return byocStartRequest{Mode: "pair", Address: address(computer), Fingerprint: computer.Fingerprint()}
		},
		byocErrUnreachable: func(*testing.T, string) byocStartRequest {
			return byocStartRequest{Mode: "pair", Address: dead, Fingerprint: "SHA256:x"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, manager, home := newJobControl(t)
			if err := startJob(t, c, setup(t, home)); err != nil {
				t.Fatal(err)
			}
			status := waitJob(t, c)
			if status.Job.Stage != "failed" || status.Job.ErrorCode != name || status.Paired {
				t.Fatalf("got %+v", *status.Job)
			}
			if manager.Snapshot().Status == account.StatusPaired {
				t.Fatal("a failed pairing changed the principal")
			}
		})
	}
}

func TestStartRejectsMalformedRequestsAtOnce(t *testing.T) {
	c, _, _ := newJobControl(t)
	for _, request := range []byocStartRequest{
		{Mode: "deploy"},
		{Mode: "deploy", Target: "-oProxyCommand=x"},
		{Mode: "deploy", Target: "host;rm"},
		{Mode: "deploy", Target: "host", Address: "a b"},
		{Mode: "pair", Address: "host"},
		{Mode: "pair", Fingerprint: "SHA256:x"},
		{Mode: "other"},
	} {
		err := startJob(t, c, request)
		var fault *control.Fault
		if !errors.As(err, &fault) {
			t.Fatalf("accepted %+v", request)
		}
	}
	if c.job() != nil {
		t.Fatal("a rejected request started a job")
	}
}

func TestDeployJobPairsWithTheKeyReadOverSSH(t *testing.T) {
	c, _, home := newJobControl(t)
	computer := byoctest.New(t)
	computer.Authorize(t, home)
	endpoint := computer.Endpoint()
	release := make(chan struct{})
	var calls [][]string
	c.ssh = func(_ context.Context, args []string, stdin string, _ io.Writer) (string, error) {
		calls = append(calls, args)
		if args[0] == "-G" {
			return "hostname " + endpoint.Host + "\n", nil
		}
		<-release
		if !strings.Contains(stdin, "port="+strconv.Itoa(endpoint.Port)) {
			t.Error("deployment script lacks the port")
		}
		return "FENNEC_HOST_KEY=" + byoc.AuthorizedKey(computer.HostSigner) + "\n", nil
	}
	if err := startJob(t, c, byocStartRequest{Mode: "deploy", Target: "admin@server", Port: endpoint.Port}); err != nil {
		t.Fatal(err)
	}
	if job := c.job(); job == nil || job.Stage != "deploying" || job.Target != "admin@server" {
		t.Fatalf("unexpected running job %+v", job)
	}
	// A second start while one runs is refused.
	var fault *control.Fault
	if err := startJob(t, c, byocStartRequest{Mode: "deploy", Target: "admin@other"}); !errors.As(err, &fault) || fault.Code != byocErrBusy {
		t.Fatalf("second start: %v", err)
	}
	close(release)
	status := waitJob(t, c)
	if status.Job.Stage != "done" || !status.Paired || status.Port != endpoint.Port || status.Host != endpoint.Host {
		t.Fatalf("unexpected status %+v job %+v", status, *status.Job)
	}
	if len(calls) != 2 || calls[1][len(calls[1])-3] != "admin@server" {
		t.Fatalf("unexpected ssh calls %q", calls)
	}
}

func TestClassifyDeployError(t *testing.T) {
	exit := func(code int) error { return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() }
	for code, want := range map[int]string{255: byocErrSSH, 69: byocErrDocker, 70: byocErrDeploy, 1: byocErrDeploy} {
		if got := classifyDeployError(errors.Join(errors.New("deployment failed"), exit(code))); got != want {
			t.Fatalf("exit %d: %s, want %s", code, got, want)
		}
	}
	if got := classifyDeployError(errors.New("cannot read the SSH configuration for x")); got != byocErrSSH {
		t.Fatal(got)
	}
}
