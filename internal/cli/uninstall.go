package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/agent"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"fennec/internal/launcher"
	"fennec/internal/runta"
	"fennec/internal/setup"
	"fennec/internal/uninstall"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

type uninstallService struct {
	mu               sync.Mutex
	home, executable string
	account          *account.Manager
	plans            map[string]uninstall.Plan
	receipt          string
	workerDone       <-chan struct{}
	workerPlan       string
}

func fileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func uninstallPreview(ctx context.Context, home, executable string, cloud bool, token string) (uninstall.Plan, error) {
	var plan uninstall.Plan
	var pending *uninstall.Record
	if uninstall.Pending(home) {
		record, err := uninstall.ReadRecord(uninstall.Marker(home))
		if err != nil {
			return plan, err
		}
		pending = &record
	}
	zdot, xdg := setup.LocalShellDirectories(ctx)
	local, err := uninstall.DiscoverLocal(home, executable, agent.ManagedEntryPaths(home), zdot, xdg)
	if err != nil {
		return plan, err
	}
	if err := validateLocalUninstall(local); err != nil {
		return plan, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return plan, err
	}
	build, err := fileSHA(executable)
	if err != nil {
		return plan, err
	}
	plan = uninstall.Plan{ID: hex.EncodeToString(nonce[:]), Local: local, DeleteCloud: cloud, SourceBuild: build, Expires: time.Now().Add(10 * time.Minute)}
	if pending != nil {
		plan.ResumeID = pending.Plan.ID
		plan.DeleteCloud = pending.Plan.DeleteCloud
		plan.Cloud = pending.Plan.Cloud
		if plan.DeleteCloud && !pending.CloudDeleted {
			if token == "" {
				return plan, &control.Fault{Code: "login_required", Message: "Sign in to finish deleting the cloud computer"}
			}
			client := runta.Client{Endpoint: "https://api.runta.com", Token: token}
			owner, err := client.CurrentUser(ctx)
			if err != nil {
				return plan, uninstallPreviewError(err)
			}
			if plan.Cloud == nil || owner != plan.Cloud.Owner {
				return plan, &control.Fault{Code: "login_required", Message: "Sign in to the account that owns this cloud computer"}
			}
			current, err := client.Get(ctx, plan.Cloud.ID)
			if err != nil {
				var api *runta.APIError
				if !errors.As(err, &api) || api.Status != 404 {
					return plan, uninstallPreviewError(err)
				}
			} else {
				if current.Owner != owner || current.Environment["FENNEC_PURPOSE"] != "shared-v1" {
					return plan, fmt.Errorf("cloud computer identity changed")
				}
				plan.Cloud = &uninstall.CloudTarget{ID: current.ID, Name: current.Name, Owner: owner, Revision: current.Revision}
			}
		}
		return plan, nil
	}
	if cloud {
		if token == "" {
			return plan, &control.Fault{Code: "login_required", Message: "Sign in before deleting the cloud computer"}
		}
		client := runta.Client{Endpoint: "https://api.runta.com", Token: token}
		owner, err := client.CurrentUser(ctx)
		if err != nil {
			return plan, uninstallPreviewError(err)
		}
		values, err := client.List(ctx)
		if err != nil {
			return plan, uninstallPreviewError(err)
		}
		for _, value := range values {
			if value.Owner == owner && value.Environment["FENNEC_PURPOSE"] == "shared-v1" && value.Status != "deleted" {
				if plan.Cloud != nil {
					return plan, fmt.Errorf("more than one Fennec cloud computer found; choose a specific computer before uninstalling")
				}
				plan.Cloud = &uninstall.CloudTarget{ID: value.ID, Name: value.Name, Owner: owner, Revision: value.Revision}
			}
		}
		if plan.Cloud == nil {
			return plan, fmt.Errorf("no Fennec cloud computer found; choose local uninstall")
		}
	}
	return plan, nil
}

func uninstallPreviewError(err error) error {
	var api *runta.APIError
	if errors.As(err, &api) && (api.Status == 401 || api.Status == 403) {
		return &control.Fault{Code: "login_required", Message: "Sign in before deleting the cloud computer"}
	}
	return err
}

func validateLocalUninstall(plan uninstall.LocalPlan) error {
	for _, entry := range plan.Entries {
		if err := launcher.ValidateUninstallEntry(entry); err != nil {
			return err
		}
	}
	for _, path := range plan.ShellFiles {
		if err := launcher.ValidateUninstallShellFile(path); err != nil {
			return err
		}
	}
	return nil
}
func installUninstallControl(home, executable string, manager *account.Manager, services *daemonServices, controller *controlplane.Controller) {
	service := &uninstallService{home: home, executable: executable, account: manager, plans: map[string]uninstall.Plan{}}
	controller.PreparationBlocked = func() bool { return uninstall.Pending(home) }
	controller.UninstallState = func() any {
		service.mu.Lock()
		path := service.receipt
		done, jobPlan := service.workerDone, service.workerPlan
		service.mu.Unlock()
		if path == "" || uninstall.Pending(home) {
			path = uninstall.Marker(home)
		}
		record, err := uninstall.ReadRecord(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return map[string]string{"stage": "failed", "error": "Unable to read uninstall progress"}
		}
		if record.Stage != "complete" && record.Stage != "failed" && record.WorkerPID > 0 && !uninstallWorkerAlive(record) {
			record.Stage = "failed"
			record.Error = "Uninstall was interrupted; retry to finish"
			record.ErrorCode = "interrupted"
		}
		if done != nil && record.Plan.ID == jobPlan && record.Stage != "complete" && record.Stage != "failed" {
			select {
			case <-done:
				record.Stage = "failed"
				record.Error = "Uninstall was interrupted; retry to finish"
				record.ErrorCode = "interrupted"
			default:
			}
		}
		return record
	}
	controller.Additional["uninstall.plan"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request struct {
			DeleteCloud bool `json:"delete_cloud"`
		}
		if json.Unmarshal(raw, &request) != nil {
			return nil, fmt.Errorf("invalid uninstall options")
		}
		_, token, _, _ := manager.Session()
		plan, err := uninstallPreview(ctx, home, executable, request.DeleteCloud, token)
		if err != nil {
			return nil, err
		}
		service.mu.Lock()
		service.plans = map[string]uninstall.Plan{plan.ID: plan}
		service.mu.Unlock()
		return plan, nil
	}
	controller.Additional["uninstall.execute"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request struct {
			PlanID string `json:"plan_id"`
			uninstall.Request
		}
		if json.Unmarshal(raw, &request) != nil {
			return nil, fmt.Errorf("invalid uninstall confirmation")
		}
		service.mu.Lock()
		defer service.mu.Unlock()
		plan, ok := service.plans[request.PlanID]
		if !ok {
			return nil, fmt.Errorf("review the uninstall preview first")
		}
		if err := uninstall.ConfirmPlan(plan, request.Request, time.Now()); err != nil {
			return nil, err
		}
		done := make(chan struct{})
		receipt, err := startUninstallWorker(plan, request.Request, 0, done)
		if err != nil {
			return nil, err
		}
		service.receipt = receipt
		service.workerDone = done
		service.workerPlan = plan.ID
		delete(service.plans, plan.ID)
		return map[string]string{"status": "started"}, nil
	}
	controller.Additional["uninstall.quiesce"] = func(ctx context.Context, _ json.RawMessage) (any, error) {
		if !uninstall.Pending(home) {
			return nil, fmt.Errorf("confirmed uninstall intent required")
		}
		if err := controller.QuiescePreparation(ctx); err != nil {
			return nil, err
		}
		done := make(chan struct{})
		go func() { services.close(); close(done) }()
		select {
		case <-done:
			return map[string]bool{"stopped": true}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type uninstallWork struct {
	Record    uninstall.Record `json:"record"`
	ParentPID int              `json:"parent_pid"`
}

func startUninstallWorker(plan uninstall.Plan, request uninstall.Request, parent int, exited ...chan struct{}) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("uninstall is available on macOS")
	}
	if err := uninstall.ConfirmPlan(plan, request, time.Now()); err != nil {
		return "", err
	}
	source := filepath.Join(plan.Local.App, "Contents/MacOS/fen")
	build, err := fileSHA(source)
	if err != nil || build != plan.SourceBuild {
		return "", fmt.Errorf("Fennec changed; review uninstall again")
	}
	dir, err := os.MkdirTemp("", "fennec-uninstall-")
	if err != nil {
		return "", err
	}
	started := false
	defer func() {
		if !started {
			os.RemoveAll(dir)
		}
	}()
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	binary := filepath.Join(dir, "fen")
	output, err := os.OpenFile(binary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	record := uninstall.Record{Plan: plan, Request: request, Stage: "preparing"}
	if previous, err := uninstall.ReadRecord(uninstall.Marker(plan.Local.Home)); err == nil {
		if !matchesPendingUninstall(plan, previous) {
			return "", fmt.Errorf("another uninstall is pending")
		}
		record.CloudDeleted = previous.CloudDeleted
	} else if !os.IsNotExist(err) {
		return "", err
	}
	work := uninstallWork{Record: record, ParentPID: parent}
	data, _ := json.Marshal(work)
	workPath := filepath.Join(dir, "work.json")
	if err = os.WriteFile(workPath, data, 0600); err != nil {
		return "", err
	}
	receipt := filepath.Join(dir, "result.json")
	if err = uninstall.WriteRecord(receipt, record); err != nil {
		return "", err
	}
	command := exec.Command(binary, "uninstall-worker", workPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return "", err
	}
	started = true
	go func() {
		if len(exited) > 0 {
			defer close(exited[0])
		}
		_ = command.Wait()
		current, err := uninstall.ReadRecord(receipt)
		if err == nil && current.Stage != "complete" && current.Stage != "failed" {
			current.Stage = "failed"
			current.Error = "Uninstall was interrupted; retry to finish"
			current.ErrorCode = "interrupted"
			_ = uninstall.WriteRecord(receipt, current)
		}
	}()
	return receipt, nil
}

func uninstallWorkerAlive(record uninstall.Record) bool {
	if record.WorkerPID <= 1 || record.WorkerPath == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/bin/ps", "-ww", "-p", fmt.Sprint(record.WorkerPID), "-o", "comm=").Output()
	return err == nil && strings.TrimSpace(string(output)) == record.WorkerPath
}

func matchesPendingUninstall(plan uninstall.Plan, pending uninstall.Record) bool {
	if plan.ResumeID != pending.Plan.ID || plan.DeleteCloud != pending.Plan.DeleteCloud {
		return false
	}
	if plan.Cloud == nil || pending.Plan.Cloud == nil {
		return plan.Cloud == nil && pending.Plan.Cloud == nil
	}
	return plan.Cloud.ID == pending.Plan.Cloud.ID && plan.Cloud.Owner == pending.Plan.Cloud.Owner
}

func uninstallCommand(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(output)
	dry := flags.Bool("dry-run", false, "Preview without changing local or cloud state")
	yes := flags.Bool("yes", false, "Confirm local uninstall")
	cloud := flags.Bool("delete-cloud", false, "Also permanently delete the cloud computer")
	confirm := flags.String("confirm-runtime", "", "Exact cloud computer ID to delete")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected uninstall arguments")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	token := ""
	needsToken := *cloud
	if pending, err := uninstall.ReadRecord(uninstall.Marker(home)); err == nil {
		needsToken = pending.Plan.DeleteCloud && !pending.CloudDeleted
	}
	if needsToken {
		token, err = (account.HelperCredentials{Path: filepath.Join(filepath.Dir(executable), "fennec-credentials")}).Read(ctx)
		if err != nil {
			return fmt.Errorf("sign in before deleting the cloud computer")
		}
	}
	plan, err := uninstallPreview(ctx, home, executable, *cloud, token)
	if err != nil {
		return err
	}
	if *dry {
		return json.NewEncoder(output).Encode(plan)
	}
	if plan.DeleteCloud && *yes && !*cloud {
		return fmt.Errorf("pending cloud deletion requires --delete-cloud and --confirm-runtime")
	}
	if !plan.DeleteCloud && *confirm != "" {
		return fmt.Errorf("--confirm-runtime requires --delete-cloud")
	}
	request := uninstall.Request{ConfirmLocal: *yes, DeleteCloud: plan.DeleteCloud, ConfirmRuntime: *confirm}
	if !*yes {
		if !isTerminal(os.Stdin) {
			return fmt.Errorf("use --dry-run to review, then --yes to uninstall")
		}
		fmt.Fprintln(output, "Uninstall Fennec and remove its local settings and credentials?")
		if plan.DeleteCloud {
			fmt.Fprintf(output, "PERMANENTLY DELETE cloud computer %s (%s), including every session and file.\nType its full ID to confirm: ", plan.Cloud.Name, plan.Cloud.ID)
		} else {
			fmt.Fprint(output, "Cloud sessions and files will be kept. Type uninstall to confirm: ")
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if plan.DeleteCloud {
			request.ConfirmRuntime = line
			request.ConfirmLocal = line == plan.Cloud.ID
		} else {
			request.ConfirmLocal = line == "uninstall"
		}
	}
	if err = uninstall.ConfirmPlan(plan, request, time.Now()); err != nil {
		return err
	}
	done := make(chan struct{})
	receipt, err := startUninstallWorker(plan, request, os.Getpid(), done)
	if err != nil {
		return err
	}
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		record, err := uninstall.ReadRecord(receipt)
		if err != nil {
			return err
		}
		if record.Stage == "complete" {
			os.RemoveAll(filepath.Dir(receipt))
			fmt.Fprintln(output, "Fennec uninstalled.")
			return nil
		}
		if record.Stage == "failed" {
			return fmt.Errorf("%s (details: %s)", record.Error, receipt)
		}
		select {
		case <-done:
			final, readErr := uninstall.ReadRecord(receipt)
			if readErr == nil && final.Stage == "complete" {
				os.RemoveAll(filepath.Dir(receipt))
				fmt.Fprintln(output, "Fennec uninstalled.")
				return nil
			}
			if readErr == nil && final.Stage == "failed" {
				return fmt.Errorf("%s (details: %s)", final.Error, receipt)
			}
			return fmt.Errorf("uninstall worker stopped; review progress at %s and retry", receipt)
		default:
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("uninstall continues; progress: %s", receipt)
		case <-timer.C:
		}
	}
}
