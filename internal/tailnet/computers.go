package tailnet

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KeyComment marks the cloud computer's key in authorized_keys files.
const KeyComment = "fennec-cloud-tailnet"

// KeyOptions restrict the key to connections from inside a tailnet
// (Tailscale's CGNAT IPv4 range and its IPv6 ULA prefix).
const KeyOptions = `from="100.64.0.0/10,fd7a:115c:a1e0::/48"`

// ensureKey returns the cloud computer's tailnet public key, creating the
// key pair on first use. The private key stays in ~/.ssh/fennec.
func ensureKey(p paths) (string, error) {
	public := p.key() + ".pub"
	if data, err := os.ReadFile(public); err == nil {
		if _, statErr := os.Stat(p.key()); statErr == nil {
			return strings.TrimSpace(string(data)), nil
		}
	}
	if err := os.MkdirAll(p.sshDir(), 0700); err != nil {
		return "", err
	}
	os.Remove(p.key())
	os.Remove(public)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", KeyComment, "-f", p.key()).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(public)
	return strings.TrimSpace(string(data)), err
}

// AuthorizeLine is the authorized_keys line for key.
func AuthorizeLine(key string) string {
	fields := strings.Fields(key)
	if len(fields) < 2 {
		return ""
	}
	return KeyOptions + " " + fields[0] + " " + fields[1] + " " + KeyComment
}

// AuthorizeCommands are the one-line commands that authorize key on a new
// computer. They are idempotent. On a Mac without Remote Login, the POSIX one
// also opens System Settings › Sharing.
func AuthorizeCommands(key string) map[string]string {
	line := AuthorizeLine(key)
	fields := strings.Fields(key)
	if line == "" {
		return nil
	}
	body := fields[1]
	posix := `mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && { grep -qF '` + body + `' ~/.ssh/authorized_keys || echo '` + line + `' >> ~/.ssh/authorized_keys; } && echo 'Fennec: cloud computer authorized' && { [ "$(uname)" != Darwin ] || nc -z localhost 22 2>/dev/null || open 'x-apple.systempreferences:com.apple.Sharing-Settings.extension'; }`
	windows := `$k='` + line + `'; $a=([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole('Administrators'); $f=if($a){"$env:ProgramData\ssh\administrators_authorized_keys"}else{"$HOME\.ssh\authorized_keys"}; New-Item -Force -ItemType Directory (Split-Path $f) | Out-Null; if(!(Test-Path $f) -or !(Select-String -Quiet -SimpleMatch '` + body + `' $f)){Add-Content -Encoding ascii $f $k}; if($a){icacls $f /inheritance:r /grant Administrators:F /grant SYSTEM:F | Out-Null}; 'Fennec: cloud computer authorized'`
	return map[string]string{"posix": posix, "windows": windows}
}

var validUser = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._@-]{0,63}$`)

// Validate checks an add or remove request.
func (r Request) Validate(needUser bool) error {
	if r.ID == "" || len(r.ID) > 64 || strings.ContainsAny(r.ID, " \t\r\n/") {
		return fmt.Errorf("invalid device")
	}
	if needUser && !validUser.MatchString(r.User) {
		return fmt.Errorf("invalid user name")
	}
	if r.Port < 0 || r.Port > 65535 || len(r.Password) > 1024 || len(r.Passphrase) > 1024 || len(r.PrivateKey) > 32<<10 {
		return fmt.Errorf("invalid sign-in details")
	}
	return nil
}

func loadComputers(p paths) []Computer {
	var list []Computer
	data, err := os.ReadFile(p.computers())
	if err == nil {
		json.Unmarshal(data, &list)
	}
	return list
}

func saveComputers(p paths, list []Computer) error {
	sort.Slice(list, func(i, j int) bool { return list[i].AddedAt < list[j].AddedAt })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p.computers()), 0700); err != nil {
		return err
	}
	tmp := p.computers() + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, p.computers()); err != nil {
		return err
	}
	return writeSSHConfig(p, list)
}

var aliasUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// alias picks the ssh alias for a computer: its tailnet name, unless another
// Fennec alias (the Mac's device alias) already uses it.
func alias(p paths, name string, list []Computer, id string) string {
	base := strings.Trim(aliasUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "computer"
	}
	used := map[string]bool{"machine": true}
	files, _ := filepath.Glob(filepath.Join(p.sshDir(), "*.conf"))
	for _, file := range files {
		if file == p.sshConfig() {
			continue
		}
		data, _ := os.ReadFile(file)
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) > 1 && strings.EqualFold(fields[0], "Host") {
				for _, host := range fields[1:] {
					used[strings.ToLower(host)] = true
				}
			}
		}
	}
	for _, c := range list {
		if c.ID != id {
			used[c.Alias] = true
		}
	}
	candidate := base
	for n := 2; used[candidate]; n++ {
		candidate = base + "-" + strconv.Itoa(n)
	}
	return candidate
}

func (p paths) proxyCommand() string {
	return strconv.Quote(p.tailscale()) + " --socket=" + strconv.Quote(p.socket()) + " nc %h %p"
}

// writeSSHConfig gives every added computer an alias in
// ~/.ssh/fennec/tailnet.conf, which ~/.ssh/config already includes for the
// Mac's device alias.
func writeSSHConfig(p paths, list []Computer) error {
	var b strings.Builder
	b.WriteString("# Computers added over Tailscale in Fennec (FEN-104). Generated; edits are overwritten.\n")
	for _, c := range list {
		fmt.Fprintf(&b, "Host %s\n  HostName %s\n  User %s\n", c.Alias, c.Host, c.User)
		if c.Port != 0 && c.Port != 22 {
			fmt.Fprintf(&b, "  Port %d\n", c.Port)
		}
		for _, option := range sshOptions(p) {
			key, value, _ := strings.Cut(option, "=")
			fmt.Fprintf(&b, "  %s %s\n", key, value)
		}
	}
	if err := os.MkdirAll(p.sshDir(), 0700); err != nil {
		return err
	}
	tmp := p.sshConfig() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p.sshConfig())
}

func sshOptions(p paths) []string {
	return []string{
		"IdentityFile=" + p.key(),
		"IdentitiesOnly=yes",
		"ProxyCommand=" + p.proxyCommand(),
		"UserKnownHostsFile=" + p.knownHosts(),
		"StrictHostKeyChecking=accept-new",
		"ControlMaster=auto",
		"ControlPath=" + filepath.Join(p.sshDir(), "tailnet-cm-%C"),
		"ControlPersist=5m",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=3",
	}
}

// probeScript prints key=value metrics on macOS and Linux. It runs as `sh -s`
// so the remote login shell doesn't matter.
const probeScript = `LC_ALL=C; export LC_ALL
os=$(uname -s)
used_disk() { df -Pk "$1" 2>/dev/null | awk 'NR==2 {printf "disk_total_bytes=%.0f\ndisk_used_bytes=%.0f\n", $2*1024, ($2-$4)*1024}'; }
if [ "$os" = Darwin ]; then
  echo os_name=macOS
  echo "os_version=$(sw_vers -productVersion 2>/dev/null)"
  echo "name=$(scutil --get ComputerName 2>/dev/null)"
  n=$(sysctl -n hw.ncpu); echo "cpu_count=$n"
  echo "memory_total_bytes=$(sysctl -n hw.memsize)"
  vm_stat | awk '/page size of/ {for(i=1;i<=NF;i++) if($i ~ /^[0-9]+$/) ps=$i} /Pages active/ {a=$3} /Pages wired down/ {w=$4} /occupied by compressor/ {c=$5} END {gsub(/\./,"",a); gsub(/\./,"",w); gsub(/\./,"",c); printf "memory_used_bytes=%.0f\n", (a+w+c)*ps}'
  ps -A -o %cpu= | awk -v n="$n" '{s+=$1} END {if (n>0) printf "cpu_percent=%.1f\n", s/n}'
  if [ -d /System/Volumes/Data ]; then used_disk /System/Volumes/Data; else used_disk /; fi
else
  if [ -r /etc/os-release ]; then . /etc/os-release; echo "os_name=${NAME:-Linux}"; echo "os_version=${VERSION_ID:-}"; else echo os_name=$os; fi
  echo "name=$(hostname 2>/dev/null)"
  echo "cpu_count=$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc)"
  awk '/^MemTotal:/ {t=$2} /^MemAvailable:/ {a=$2} END {if (t>0) printf "memory_total_bytes=%.0f\nmemory_used_bytes=%.0f\n", t*1024, (t-a)*1024}' /proc/meminfo 2>/dev/null
  s1=$(awk '/^cpu / {print $2+$3+$4+$5+$6+$7+$8, $5+$6}' /proc/stat); sleep 0.5; s2=$(awk '/^cpu / {print $2+$3+$4+$5+$6+$7+$8, $5+$6}' /proc/stat)
  echo "$s1 $s2" | awk '{dt=$3-$1; di=$4-$2; if (dt>0) printf "cpu_percent=%.1f\n", (dt-di)*100/dt}'
  used_disk /
fi
`

func parseMetrics(out []byte) *Metrics {
	m := &Metrics{}
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		number, _ := strconv.ParseFloat(value, 64)
		switch key {
		case "name":
			m.Name = value
		case "os_name":
			m.OSName, found = value, true
		case "os_version":
			m.OSVersion = value
		case "cpu_count":
			m.CPUCount = int(number)
		case "cpu_percent":
			m.CPUPercent = number
		case "memory_total_bytes":
			m.MemTotal = uint64(number)
		case "memory_used_bytes":
			m.MemUsed = uint64(number)
		case "disk_total_bytes":
			m.DiskTotal = uint64(number)
		case "disk_used_bytes":
			m.DiskUsed = uint64(number)
		}
	}
	if !found {
		return nil
	}
	return m
}

// probe signs in to user@host over the tailnet and reads metrics. A computer
// whose shell isn't POSIX (Windows) still counts as reached, without metrics.
func probe(ctx context.Context, p paths, host string, port int, user string) (*Metrics, string) {
	metrics, code, _ := probeDetail(ctx, p, host, port, user)
	return metrics, code
}

// probeDetail is probe plus ssh's own words when it fails (FEN-147).
func probeDetail(ctx context.Context, p paths, host string, port int, user string) (*Metrics, string, string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "LogLevel=ERROR"}
	for _, option := range sshOptions(p) {
		args = append(args, "-o", option)
	}
	if port != 0 && port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	args = append(args, "-l", user, host, "sh -s")
	command := exec.CommandContext(ctx, "ssh", args...)
	command.Stdin = strings.NewReader(probeScript)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.WaitDelay = time.Second
	out, err := command.Output()
	if err == nil {
		return parseMetrics(out), "", ""
	}
	text := strings.ToLower(stderr.String())
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() != 255 && ctx.Err() == nil {
		// The remote command ran: signed in, but the shell isn't POSIX.
		return parseMetrics(out), "", ""
	}
	detail := strings.TrimSpace(err.Error() + clip(stderr.Bytes()))
	switch {
	case strings.Contains(text, "permission denied"):
		return nil, ErrAuthFailed, detail
	case strings.Contains(text, "connection refused") || strings.Contains(text, "connect to") && strings.Contains(text, "refused"):
		return nil, ErrRefused, detail
	case strings.Contains(text, "host key") && (strings.Contains(text, "changed") || strings.Contains(text, "verification failed")):
		return nil, ErrHostKeyChange, detail
	case ctx.Err() != nil || strings.Contains(text, "timed out") || strings.Contains(text, "no route") || strings.Contains(text, "unreachable") || strings.Contains(text, "closed by remote host"):
		return nil, ErrUnreachable, detail
	}
	return nil, ErrSSHFailed, detail
}

func portOr22(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

var computersMu sync.Mutex

// Add checks that the cloud computer can sign in to the device as user and
// remembers it. The result is the device with its Computer, or an error code
// in Computer.Error when signing in failed (nothing is saved then).
func Add(ctx context.Context, home string, r Request) (Device, error) {
	if err := r.Validate(true); err != nil {
		return Device{}, err
	}
	p := paths{home}
	status := ReadStatus(ctx, home, false)
	if status.State != "running" {
		return Device{}, fmt.Errorf("tailscale is not signed in")
	}
	var device *Device
	for i := range status.Devices {
		if status.Devices[i].ID == r.ID {
			device = &status.Devices[i]
		}
	}
	if device == nil || len(device.IPs) == 0 {
		return Device{}, fmt.Errorf("device not found in the tailnet")
	}
	host := device.IPs[0]
	port := r.Port
	if port == 22 {
		port = 0
	}
	// Tailscale SSH needs no password or key, only a policy that lets this
	// sign-in through without a browser check.
	if r.Password == "" && r.PrivateKey == "" && device.TailscaleSSH {
		port = 0
		if code, url := tailscaleSSHCheck(ctx, p, host, r.User); code != "" {
			device.Computer = &Computer{ID: r.ID, Name: device.Name, Host: host, User: r.User, Checked: time.Now().Unix(), Error: code, CheckURL: url}
			return *device, nil
		}
	}
	// With a password or private key, sign in once with it to authorize the
	// cloud computer's key there; then check the key works like any add.
	if r.Password != "" || r.PrivateKey != "" {
		if code, detail := authorizeDetail(ctx, p, host, portOr22(port), r, device.OS == "windows"); code != "" {
			log.Printf("tailnet: adding %s@%s: %s: %s", r.User, host, code, detail)
			device.Computer = &Computer{ID: r.ID, Name: device.Name, Host: host, User: r.User, Port: port, Checked: time.Now().Unix(), Error: code, Detail: detail}
			return *device, nil
		}
	}
	metrics, code, detail := probeDetail(ctx, p, host, port, r.User)
	now := time.Now().Unix()
	if code != "" {
		log.Printf("tailnet: checking %s@%s after adding: %s: %s", r.User, host, code, detail)
		device.Computer = &Computer{ID: r.ID, Name: device.Name, Host: host, User: r.User, Port: port, Checked: now, Error: code, Detail: detail}
		return *device, nil
	}
	computersMu.Lock()
	defer computersMu.Unlock()
	list := loadComputers(p)
	entry := Computer{ID: r.ID, Name: device.Name, Host: host, User: r.User, Port: port, AddedAt: now}
	kept := list[:0]
	for _, c := range list {
		if c.ID == r.ID {
			entry.AddedAt = c.AddedAt
			continue
		}
		kept = append(kept, c)
	}
	entry.Alias = alias(p, device.Name, kept, r.ID)
	if err := saveComputers(p, append(kept, entry)); err != nil {
		return Device{}, err
	}
	entry.Checked, entry.Metrics = now, metrics
	rememberProbe(entry.ID, entry.Metrics, "")
	device.Computer = &entry
	return *device, nil
}

// Remove forgets a computer. Its authorized_keys entry stays on that computer
// until the user removes it there.
func Remove(ctx context.Context, home string, r Request) (Status, error) {
	if err := r.Validate(false); err != nil {
		return Status{}, err
	}
	p := paths{home}
	computersMu.Lock()
	list := loadComputers(p)
	kept := list[:0]
	for _, c := range list {
		if c.ID != r.ID {
			kept = append(kept, c)
		}
	}
	err := saveComputers(p, kept)
	computersMu.Unlock()
	if err != nil {
		return Status{}, err
	}
	forgetProbe(r.ID)
	return ReadStatus(ctx, home, false), nil
}

type probeResult struct {
	at      time.Time
	metrics *Metrics
	err     string
	url     string // Tailscale SSH check URL with err ssh_check
}

var (
	probesMu sync.Mutex
	probes   = map[string]probeResult{}
	probing  = map[string]bool{} // computers with an SSH check under way
)

func rememberProbe(id string, metrics *Metrics, code string) {
	probesMu.Lock()
	probes[id] = probeResult{at: time.Now(), metrics: metrics, err: code}
	probesMu.Unlock()
}

func forgetProbe(id string) {
	probesMu.Lock()
	delete(probes, id)
	probesMu.Unlock()
}

// probeInterval limits how often one computer is signed in to for metrics;
// the panel asks every few seconds while Computers is open.
const probeInterval = 10 * time.Second

// attachComputers marks the tailnet's added computers. With refresh, due
// computers are checked over SSH; unless wait, the checks run in the
// background and a later status shows their result (FEN-147).
func attachComputers(ctx context.Context, p paths, status *Status, refresh, wait bool) {
	list := loadComputers(p)
	byID := map[string]Computer{}
	for _, c := range list {
		byID[c.ID] = c
	}
	var done sync.WaitGroup
	for i := range status.Devices {
		device := &status.Devices[i]
		saved, ok := byID[device.ID]
		if !ok {
			continue
		}
		c := saved
		if len(device.IPs) > 0 && device.IPs[0] != c.Host {
			c.Host = device.IPs[0]
		}
		probesMu.Lock()
		last, seen := probes[c.ID]
		due := refresh && device.Online && !probing[c.ID] && (!seen || time.Since(last.at) >= probeInterval)
		if due {
			probing[c.ID] = true
		}
		probesMu.Unlock()
		if seen {
			c.Checked, c.Metrics, c.Error, c.CheckURL = last.at.Unix(), last.metrics, last.err, last.url
		}
		device.Computer = &c
		if !due {
			continue
		}
		tailscaleSSH := device.TailscaleSSH
		run := func(device *Device, c Computer) {
			probeCtx := context.Background()
			if wait {
				probeCtx = ctx
			}
			metrics, code := probe(probeCtx, p, c.Host, c.Port, c.User)
			url := ""
			if code == ErrUnreachable && tailscaleSSH {
				// A "check" rule holds the sign-in until it times out; ask
				// Tailscale SSH why, so the panel can say so.
				if check, link := tailscaleSSHCheck(probeCtx, p, c.Host, c.User); check == ErrSSHCheck {
					code, url = check, link
				}
			}
			probesMu.Lock()
			probes[c.ID] = probeResult{at: time.Now(), metrics: metrics, err: code, url: url}
			probesMu.Unlock()
			c.CheckURL = url
			probesMu.Lock()
			delete(probing, c.ID)
			probesMu.Unlock()
			if wait {
				c.Checked, c.Metrics, c.Error = time.Now().Unix(), metrics, code
				device.Computer = &c
			}
		}
		if !wait {
			go run(nil, c)
			continue
		}
		done.Add(1)
		go func(device *Device, c Computer) {
			defer done.Done()
			run(device, c)
		}(device, c)
	}
	done.Wait()
	for _, device := range status.Devices {
		if device.Computer != nil && device.Computer.Host != byID[device.ID].Host {
			// A peer's tailnet IP is stable, but keep the alias right if it moves.
			computersMu.Lock()
			fresh := loadComputers(p)
			for i := range fresh {
				if fresh[i].ID == device.ID {
					fresh[i].Host = device.Computer.Host
				}
			}
			saveComputers(p, fresh)
			computersMu.Unlock()
		}
	}
}
