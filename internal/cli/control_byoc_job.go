package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/byoc"
	"fennec/internal/control"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// byocJob is a deployment or pairing the menu bar started. Deploying pulls an
// image and starts a container, which outlasts one control call, so the
// panel starts it with byoc.start and follows it through byoc.status.
// ErrorCode is one of the codes below; the panel shows its own copy for each
// and never the raw SSH or Docker output.
type byocJob struct {
	ID        uint64    `json:"id"`
	Kind      string    `json:"kind"`  // "deploy" or "pair"
	Stage     string    `json:"stage"` // "deploying", "pairing", "done" or "failed"
	ErrorCode string    `json:"error_code,omitempty"`
	Target    string    `json:"target,omitempty"`
	Started   time.Time `json:"started"`
}

// Error codes reported in byocJob.ErrorCode.
const (
	byocErrInvalid           = "invalid"            // the request itself is malformed
	byocErrSSH               = "ssh_failed"         // the user's SSH could not reach or log in to the server
	byocErrDocker            = "docker_unavailable" // no usable Docker for the SSH user on the server
	byocErrDeploy            = "deploy_failed"      // the container did not start or report its key
	byocErrUnreachable       = "unreachable"        // the paired address does not answer
	byocErrFingerprint       = "fingerprint_mismatch"
	byocErrNotAuthorized     = "not_authorized" // this Mac's key is not authorized on the computer
	byocErrNotFennec         = "not_fennec"     // the server is not running the Fennec cloud image
	byocErrAlreadyPaired     = "already_paired"
	byocErrPairingUnreadable = "pairing_unreadable"
	byocErrPairing           = "pairing_failed"
	byocErrBusy              = "busy"
)

const byocJobLimit = 6 * time.Minute

type byocStartRequest struct {
	Mode string `json:"mode"` // "deploy" or "pair"
	// Deploy: an SSH destination ([user@]host or an ssh config alias), the
	// published port, and optionally where the Mac reaches the server.
	Target  string `json:"target"`
	Port    int    `json:"port"`
	Address string `json:"address"`
	// Pair: an address (host[:port]) and the host key fingerprint.
	Fingerprint string `json:"fingerprint"`
}

func (c *byocControl) job() *byocJob {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	if c.current == nil {
		return nil
	}
	copy := *c.current
	return &copy
}

func (c *byocControl) setJob(id uint64, stage, code string) {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	if c.current != nil && c.current.ID == id {
		c.current.Stage, c.current.ErrorCode = stage, code
	}
}

// start validates the request synchronously, so a typo is answered at once,
// then runs the deployment or pairing in the background.
func (c *byocControl) start(_ context.Context, raw json.RawMessage) (any, error) {
	var request byocStartRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid request"}
	}
	var work func(ctx context.Context, id uint64) (string, string)
	job := byocJob{Kind: request.Mode, Started: time.Now().UTC()}
	switch request.Mode {
	case "deploy":
		args := []string{strings.TrimSpace(request.Target)}
		if request.Port != 0 {
			args = append(args, "--port", strconv.Itoa(request.Port))
		}
		if address := strings.TrimSpace(request.Address); address != "" {
			args = append(args, "--address", address)
		}
		deploy, err := parseBYOCDeploy(args)
		if err != nil {
			return nil, &control.Fault{Code: byocErrInvalid, Message: "Enter an SSH destination such as you@server"}
		}
		if request.Address != "" {
			if _, _, err := splitBYOCAddress(strings.TrimSpace(request.Address)); err != nil {
				return nil, &control.Fault{Code: byocErrInvalid, Message: "Invalid address"}
			}
		}
		job.Stage, job.Target = "deploying", deploy.Target
		work = func(ctx context.Context, id uint64) (string, string) { return c.runDeploy(ctx, id, deploy) }
	case "pair":
		host, port, err := splitBYOCAddress(strings.TrimSpace(request.Address))
		fingerprint := strings.TrimSpace(request.Fingerprint)
		if err != nil || fingerprint == "" {
			return nil, &control.Fault{Code: byocErrInvalid, Message: "Enter the server address and its host key fingerprint"}
		}
		pair := byocPairRequest{Host: host, Port: port, User: byoc.DefaultUser, Fingerprint: fingerprint}
		job.Stage, job.Target = "pairing", netAddress(host, port)
		work = func(ctx context.Context, _ uint64) (string, string) { return c.runPair(ctx, pair) }
	default:
		return nil, &control.Fault{Code: "invalid_params", Message: "Unknown mode"}
	}
	c.jobMu.Lock()
	if c.current != nil && (c.current.Stage == "deploying" || c.current.Stage == "pairing") {
		c.jobMu.Unlock()
		return nil, &control.Fault{Code: byocErrBusy, Message: "A deployment is already running", Retryable: true}
	}
	c.nextJob++
	job.ID = c.nextJob
	c.current = &job
	c.jobMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), byocJobLimit)
		defer cancel()
		stage, code := work(ctx, job.ID)
		c.setJob(job.ID, stage, code)
	}()
	return c.snapshot(), nil
}

func (c *byocControl) runDeploy(ctx context.Context, id uint64, request byocDeployRequest) (string, string) {
	signer, err := byoc.DeviceKey(c.home)
	if err != nil {
		return "failed", byocErrDeploy
	}
	request.Deployment.DeviceKey = byoc.AuthorizedKey(signer)
	pair, err := deployOverSSH(ctx, request, c.ssh, io.Discard)
	if err != nil {
		return "failed", classifyDeployError(err)
	}
	c.setJob(id, "pairing", "")
	return c.runPair(ctx, pair)
}

func (c *byocControl) runPair(ctx context.Context, request byocPairRequest) (string, string) {
	if _, fault := c.pairWith(ctx, request); fault != nil {
		return "failed", classifyPairFault(fault)
	}
	return "done", ""
}

// daemonSSH runs the user's OpenSSH for the daemon, which has no terminal: it
// must fail rather than wait for a password, passphrase or host key prompt.
func daemonSSH(ctx context.Context, args []string, stdin string, stderr io.Writer) (string, error) {
	return systemSSH(ctx, append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}, args...), stdin, stderr)
}

func classifyDeployError(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 255:
			return byocErrSSH
		case 69:
			return byocErrDocker
		}
		return byocErrDeploy
	}
	if strings.Contains(err.Error(), "SSH configuration") || strings.Contains(err.Error(), "host name") {
		return byocErrSSH
	}
	return byocErrDeploy
}

func classifyPairFault(fault *control.Fault) string {
	switch fault.Code {
	case byocErrAlreadyPaired, byocErrPairingUnreadable:
		return fault.Code
	case "invalid_params":
		return byocErrInvalid
	}
	message := fault.Message
	switch {
	case strings.Contains(message, "does not match the verified fingerprint"):
		return byocErrFingerprint
	case strings.Contains(message, "unable to authenticate"):
		return byocErrNotAuthorized
	case strings.Contains(message, "no Fennec identity"), strings.Contains(message, "identity is invalid"):
		return byocErrNotFennec
	case strings.Contains(message, "connection refused"), strings.Contains(message, "i/o timeout"),
		strings.Contains(message, "no such host"), strings.Contains(message, "deadline exceeded"),
		strings.Contains(message, "network is unreachable"), strings.Contains(message, "host is down"):
		return byocErrUnreachable
	}
	return byocErrPairing
}
