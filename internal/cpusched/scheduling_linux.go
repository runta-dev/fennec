//go:build linux

package cpusched

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const groupRoot = "/sys/fs/cgroup/fennec"

func groups() error {
	// Root's CPU controller must already be delegated by the runtime. Do not
	// change an ancestor or relocate unrelated processes to make it available.
	for _, dir := range []string{groupRoot, groupRoot + "/control", groupRoot + "/workload"} {
		if err := os.Mkdir(dir, 0755); err != nil && !os.IsExist(err) {
			return err
		}
		if dir == groupRoot {
			if err := os.WriteFile(dir+"/cgroup.subtree_control", []byte("+cpu"), 0644); err != nil {
				return err
			}
		}
	}
	for name, weight := range map[string]string{"control": "1000", "workload": "100"} {
		if err := os.WriteFile(groupRoot+"/"+name+"/cpu.weight", []byte(weight), 0644); err != nil {
			return err
		}
	}
	return nil
}

func place(pid int, group string, nice int) error {
	var failures []string
	if err := os.WriteFile(groupRoot+"/"+group+"/cgroup.procs", []byte(strconv.Itoa(pid)), 0644); err != nil {
		failures = append(failures, err.Error())
	}
	// Linux nice is per-thread. Updating only the process leader misses Go's
	// existing worker threads and gives misleading ps output.
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return err
	}
	for _, task := range tasks {
		tid, err := strconv.Atoi(task.Name())
		if err != nil {
			continue
		}
		if err := unix.Setpriority(unix.PRIO_PROCESS, tid, nice); err != nil && err != unix.ESRCH {
			failures = append(failures, err.Error())
			break
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("pid %d: %s", pid, strings.Join(failures, "; "))
	}
	return nil
}

type process struct {
	pid, parent int
	exe, mode   string
	start       string
}

func processes() map[int]process {
	entries, _ := os.ReadDir("/proc")
	result := map[int]process{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		base := "/proc/" + entry.Name()
		exe, err := os.Readlink(base + "/exe")
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(base + "/stat")
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 20 {
			continue
		}
		parent, _ := strconv.Atoi(fields[1])
		args, _ := os.ReadFile(base + "/cmdline")
		parts := strings.Split(string(args), "\x00")
		mode := ""
		if len(parts) > 1 {
			mode = parts[1]
		}
		result[pid] = process{pid, parent, strings.TrimSuffix(exe, " (deleted)"), mode, fields[19]}
	}
	return result
}

func classify(home string, all map[int]process, self int) map[int]string {
	classes := map[int]string{}
	herdr, _ := filepath.EvalSymlinks(filepath.Join(home, ".local/bin/herdr-core"))
	terminal, _ := filepath.EvalSymlinks(filepath.Join(home, ".local/fennec/bin/fennec-terminal"))
	for pid, p := range all {
		isTerminal := p.exe == terminal || strings.HasPrefix(p.exe, filepath.Join(home, ".local/fennec/terminal")+"/") && filepath.Base(p.exe) == "fennec-terminal"
		if p.exe == herdr && herdr != "" || isTerminal && (p.mode == "serve" || p.mode == "serve-initial" || p.mode == "attach" || p.mode == "mac-relay") {
			classes[pid] = "control"
		} else if strings.HasPrefix(p.exe, filepath.Join(home, ".local/fennec")+"/") {
			switch filepath.Base(p.exe) {
			case "codex", "codex-code-mode-host", "claude", "opencode":
				// Detached app-servers no longer have a Herdr ancestor.
				classes[pid] = "workload"
			}
		}
	}
	classes[self] = "control"
	// Only the resident's verified SSH ancestor chain is elevated.
	for p, ok := all[self]; ok && p.parent > 1; p, ok = all[p.parent] {
		parent := all[p.parent]
		if parent.exe == "/usr/sbin/sshd" {
			classes[parent.pid] = "control"
		} else {
			break
		}
	}
	// Existing agent trees originate at a verified Herdr server. Stop at a
	// separately classified terminal helper; never sweep every root process.
	for changed := true; changed; {
		changed = false
		for pid, p := range all {
			if classes[pid] != "" {
				continue
			}
			parent := all[p.parent]
			if classes[p.parent] == "workload" || parent.exe == herdr && herdr != "" {
				classes[pid] = "workload"
				changed = true
			}
		}
	}
	return classes
}

// Interactive is best-effort and persists a private, credential-free report.
// Existing sessions are moved, never restarted. New descendants are fenced by
// ExecWorkload even when Herdr already existed before this resident started.
func Interactive(home string) Report {
	report := Report{ControlGroup: groupRoot + "/control", WorkloadGroup: groupRoot + "/workload"}
	if err := groups(); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	all := processes()
	for pid, group := range classify(home, all, os.Getpid()) {
		// A short-lived agent may exit while this snapshot is being applied.
		// Fence PID reuse before touching a different process's scheduling.
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 20 || fields[19] != all[pid].start {
			continue
		}
		nice := 0
		if group == "control" {
			nice = -5
		}
		if err := place(pid, group, nice); err != nil {
			if len(report.Errors) < 12 {
				report.Errors = append(report.Errors, err.Error())
			}
			continue
		}
		if group == "control" {
			report.ControlProcesses++
		} else {
			report.WorkloadProcesses++
		}
	}
	dir := filepath.Join(home, ".local/fennec/diagnostics")
	if os.MkdirAll(dir, 0700) == nil {
		data, _ := json.Marshal(report)
		if f, err := os.CreateTemp(dir, "cpu-scheduling-*"); err == nil {
			name := f.Name()
			_, err = f.Write(data)
			closeErr := f.Close()
			if err == nil && closeErr == nil {
				_ = os.Rename(name, filepath.Join(dir, "cpu-scheduling.json"))
			}
			_ = os.Remove(name)
		}
	}
	return report
}

func ExecWorkload(command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("workload command required")
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Never fail an agent launch because the host does not delegate cgroups.
	_ = groups()
	_ = place(os.Getpid(), "workload", 0)
	return unix.Exec(path, command, os.Environ())
}
