//go:build linux

package cpusched

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// groupRoot holds control (the terminal control plane) and workload. Workload
// holds no processes itself, only groups: shared (agent servers, the model
// gateway, command streams) and one per session (workload/<id>, the agent
// and everything it starts). A session the user is typing or scrolling in is
// weighted above the others (Focus), so its agent redraws first when agents
// compete for CPU; weights only matter under contention.
var groupRoot = "/sys/fs/cgroup/fennec"

// Tests run the resident's entrypoints (traffic-broker, workload-exec) as
// subprocesses of the test binary; they must never reshape the host's groups.
func init() {
	if root := os.Getenv("FENNEC_CPU_GROUP_ROOT"); root != "" {
		groupRoot = root
	} else if exe, err := os.Executable(); err == nil && strings.HasSuffix(filepath.Base(exe), ".test") {
		// The running executable, not os.Args[0]: tests also run the test
		// binary through a link named fennec-terminal.
		groupRoot = filepath.Join(os.TempDir(), "fennec-cpusched-test")
	}
}

const (
	sharedGroup   = "workload/shared"
	sessionWeight = "100"
	focusWeight   = "1000"
)

var sessionID = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)

// sessionGroup is the group of a session ID, "" when the ID is not one.
func sessionGroup(id string) string {
	if !sessionID.MatchString(id) {
		return ""
	}
	return "workload/" + id
}

// ensureGroup creates a workload group. A new group already has the kernel's
// default weight, 100 (sessionWeight); its cpu.weight does not even exist
// until the parent enables the cpu controller, so it is not written here.
func ensureGroup(group string) error {
	if err := os.Mkdir(groupRoot+"/"+group, 0755); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

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
	return splitWorkload()
}

// splitWorkload turns workload into a parent of groups: what runs in it
// directly moves to shared, then its children get the cpu controller.
// Retried, since a process may be placed in workload meanwhile.
func splitWorkload() error {
	if err := ensureGroup(sharedGroup); err != nil {
		return err
	}
	dir := groupRoot + "/workload"
	var err error
	for try := 0; try < 5; try++ {
		if enabled, _ := os.ReadFile(dir + "/cgroup.subtree_control"); strings.Contains(string(enabled), "cpu") {
			return nil
		}
		procs, _ := os.ReadFile(dir + "/cgroup.procs")
		for _, pid := range strings.Fields(string(procs)) {
			_ = os.WriteFile(groupRoot+"/"+sharedGroup+"/cgroup.procs", []byte(pid), 0644)
		}
		if err = os.WriteFile(dir+"/cgroup.subtree_control", []byte("+cpu"), 0644); err == nil {
			return nil
		}
	}
	return err
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
	cwd         string
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
		group, _ := os.ReadFile(base + "/cgroup")
		if strings.Contains(string(group), "/fennec-magpie.service") || strings.Contains(string(group), "/fennec-magpie-health.service") {
			// systemd owns this entire service tree, including plugin agent children.
			continue
		}
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
		cwd, _ := os.Readlink(base + "/cwd")
		result[pid] = process{pid, parent, strings.TrimSuffix(exe, " (deleted)"), mode, fields[19], cwd}
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
		if parent.exe == "/usr/sbin/sshd" && parent.parent > 1 {
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
	// A tree started by Herdr belongs to the session whose directory its root
	// runs in (Herdr launches each agent in ~/.fen/<id>); the rest is shared.
	workload := map[int]bool{}
	for pid, class := range classes {
		workload[pid] = class == "workload"
	}
	for pid := range workload {
		if !workload[pid] {
			continue
		}
		root := all[pid]
		for seen := 0; seen < 64 && workload[root.parent]; seen++ {
			root = all[root.parent]
		}
		group := sharedGroup
		if herdr != "" && all[root.parent].exe == herdr {
			if id, ok := strings.CutPrefix(root.cwd, filepath.Join(home, ".fen")+"/"); ok {
				if g := sessionGroup(id); g != "" {
					group = g
				}
			}
		}
		classes[pid] = group
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
	placement := classify(home, all, os.Getpid())
	sessions := map[string]bool{}
	for _, group := range placement {
		if strings.HasPrefix(group, "workload/") && group != sharedGroup && !sessions[group] {
			sessions[group] = true
			if err := ensureGroup(group); err != nil && len(report.Errors) < 12 {
				report.Errors = append(report.Errors, err.Error())
			}
		}
	}
	report.SessionGroups = len(sessions)
	for pid, group := range placement {
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
	pruneSessions(sessions)
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
	session := ""
	if len(command) >= 2 && command[0] == "--session" {
		session, command = command[1], command[2:]
	}
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
	_ = placeWorkload(os.Getpid(), session)
	return unix.Exec(path, command, os.Environ())
}

// pruneSessions removes session groups with no process left, except those
// just placed. The kernel never removes an empty group, and refuses to
// remove one that still has a process, so a group in use is never lost; one
// removed just as an agent starts is created again (placeWorkload).
func pruneSessions(keep map[string]bool) {
	entries, _ := os.ReadDir(groupRoot + "/workload")
	for _, entry := range entries {
		group := "workload/" + entry.Name()
		if !entry.IsDir() || group == sharedGroup || keep[group] || sessionGroup(entry.Name()) == "" {
			continue
		}
		if events, err := os.ReadFile(groupRoot + "/" + group + "/cgroup.events"); err == nil && strings.Contains(string(events), "populated 1") {
			continue
		}
		_ = os.Remove(groupRoot + "/" + group)
	}
}

func prune() { pruneSessions(nil) }

// placeWorkload puts a starting process in its session's group, or shared.
// Creating the group and placing are retried, since a sweep may remove an
// empty group in between.
func placeWorkload(pid int, session string) error {
	group := sessionGroup(session)
	if group == "" {
		return place(pid, sharedGroup, 0)
	}
	var err error
	for try := 0; try < 3; try++ {
		if err = ensureGroup(group); err == nil {
			if err = place(pid, group, 0); err == nil {
				return nil
			}
		}
	}
	return place(pid, sharedGroup, 0)
}

// setFocus writes the weight of a session's group; false when the session
// has no group (an agent started before session groups, or none at all).
func setFocus(id string, on bool) bool {
	group := sessionGroup(id)
	if group == "" {
		return false
	}
	weight := sessionWeight
	if on {
		weight = focusWeight
	}
	return os.WriteFile(groupRoot+"/"+group+"/cpu.weight", []byte(weight), 0644) == nil
}
