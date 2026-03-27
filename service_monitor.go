package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceMonitorConfig is loaded from service-monitor.json.
type ServiceMonitorConfig struct {
	Interval      int              `json:"interval"`
	SyncthingURL  string           `json:"syncthing_url"`
	SyncthingKey  string           `json:"syncthing_key"`
	SyncthingHost string           `json:"syncthing_host"`
	ChezmoiRepoOn string          `json:"chezmoi_repo_on"`
	HTTPChecks    []HTTPCheck      `json:"http_checks"`
	DockerHost    string           `json:"docker_host"`
	Tunnels       []TunnelCheck    `json:"tunnels"`
	SyncFolders   []string         `json:"sync_folders"`
}

type HTTPCheck struct {
	Name   string            `json:"name"`
	URL    string            `json:"url"`
	Port   int               `json:"port"`
	Headers map[string]string `json:"headers,omitempty"`
}

type TunnelCheck struct {
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Backend  string `json:"backend"`
}

// ServiceStatus is the full snapshot published to NATS and served via API.
type ServiceStatus struct {
	Timestamp   string         `json:"timestamp"`
	Services    []ServiceEntry `json:"services"`
	Docker      []DockerEntry  `json:"docker"`
	Tunnels     []TunnelEntry  `json:"tunnels"`
	Sync        SyncStatus     `json:"sync"`
	Chezmoi     ChezmoiStatus  `json:"chezmoi"`
	FailedUnits []string       `json:"failed_units"`
}

type ServiceEntry struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // "up", "down", "degraded"
	Port     int    `json:"port,omitempty"`
	Latency  int    `json:"latency_ms"`
	Detail   string `json:"detail,omitempty"`
}

type DockerEntry struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // "running", "stopped", "unhealthy"
	Health   string `json:"health,omitempty"`
	Uptime   string `json:"uptime"`
	Port     string `json:"port,omitempty"`
	Restarts int    `json:"restarts"`
}

type TunnelEntry struct {
	Name       string `json:"name"`
	Hostname   string `json:"hostname"`
	Status     string `json:"status"` // "up", "down"
	Latency    int    `json:"latency_ms"`
	TunnelConns int   `json:"tunnel_conns"`
}

type SyncStatus struct {
	Connected bool         `json:"connected"`
	Folders   []SyncFolder `json:"folders"`
	Conflicts int          `json:"conflicts"`
}

type SyncFolder struct {
	ID        string  `json:"id"`
	State     string  `json:"state"`
	Files     int     `json:"files"`
	NeedFiles int     `json:"need_files"`
	SizeGB    float64 `json:"size_gb"`
	LastScan  string  `json:"last_scan"`
	Conflicts int     `json:"conflicts"`
}

type ChezmoiStatus struct {
	LastCommit string `json:"last_commit"`
	Modified   int    `json:"modified"`
	Added      int    `json:"added"`
}

func loadServiceMonitorConfig() ServiceMonitorConfig {
	smc := ServiceMonitorConfig{Interval: 30}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".config", "claude-peers", "service-monitor.json")
	if p := os.Getenv("SERVICE_MONITOR_CONFIG"); p != "" {
		path = p
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return smc
	}
	json.Unmarshal(data, &smc)
	return smc
}

// runServiceMonitor starts the service monitor loop and exposes data via the Gridwatch API.
func (gw *Gridwatch) runServiceMonitor(ctx context.Context) {
	smc := loadServiceMonitorConfig()
	if len(smc.HTTPChecks) == 0 && smc.DockerHost == "" && smc.SyncthingURL == "" {
		log.Printf("[svcmon] no service monitor config, skipping")
		return
	}

	interval := time.Duration(smc.Interval) * time.Second
	log.Printf("[svcmon] monitoring %d services, %d tunnels, interval %s",
		len(smc.HTTPChecks), len(smc.Tunnels), interval)

	var prev *ServiceStatus
	for {
		status := collectServices(smc)
		data, _ := json.Marshal(status)
		gw.svcMu.Lock()
		gw.svcCache = data
		gw.svcMu.Unlock()

		gw.emitServiceTickerEvents(&status, prev)
		prev = &status

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func collectServices(smc ServiceMonitorConfig) ServiceStatus {
	status := ServiceStatus{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	var wg sync.WaitGroup

	// HTTP health checks.
	services := make([]ServiceEntry, len(smc.HTTPChecks))
	for i, check := range smc.HTTPChecks {
		wg.Add(1)
		go func(i int, c HTTPCheck) {
			defer wg.Done()
			services[i] = checkHTTP(c)
		}(i, check)
	}

	// Tunnel checks.
	tunnels := make([]TunnelEntry, len(smc.Tunnels))
	for i, t := range smc.Tunnels {
		wg.Add(1)
		go func(i int, t TunnelCheck) {
			defer wg.Done()
			tunnels[i] = checkTunnel(t)
		}(i, t)
	}

	// Docker (sequential, single SSH/exec call).
	var docker []DockerEntry
	var dockerDone sync.WaitGroup
	dockerDone.Add(1)
	go func() {
		defer dockerDone.Done()
		docker = checkDocker(smc.DockerHost)
	}()

	// Syncthing.
	var syncStatus SyncStatus
	var syncDone sync.WaitGroup
	syncDone.Add(1)
	go func() {
		defer syncDone.Done()
		if smc.SyncthingHost != "" {
			syncStatus = checkSyncthingSSH(smc.SyncthingHost, smc.SyncthingURL, smc.SyncthingKey, smc.SyncFolders)
		} else {
			syncStatus = checkSyncthing(smc.SyncthingURL, smc.SyncthingKey, smc.SyncFolders)
		}
	}()

	// Chezmoi.
	var chezmoi ChezmoiStatus
	var chezDone sync.WaitGroup
	chezDone.Add(1)
	go func() {
		defer chezDone.Done()
		chezmoi = checkChezmoi(smc.ChezmoiRepoOn)
	}()

	// Failed systemd units (via DockerHost SSH or local).
	var failedUnits []string
	var failedDone sync.WaitGroup
	failedDone.Add(1)
	go func() {
		defer failedDone.Done()
		failedUnits = checkFailedUnits(smc.DockerHost)
	}()

	wg.Wait()
	dockerDone.Wait()
	syncDone.Wait()
	chezDone.Wait()
	failedDone.Wait()

	status.Services = services
	status.Docker = docker
	status.Tunnels = tunnels
	status.Sync = syncStatus
	status.Chezmoi = chezmoi
	status.FailedUnits = failedUnits
	return status
}

// emitServiceTickerEvents compares current vs previous service status and pushes changes to the ticker.
func (gw *Gridwatch) emitServiceTickerEvents(current, prev *ServiceStatus) {
	if gw.ticker == nil {
		return
	}

	prevSvc := make(map[string]string)
	prevDocker := make(map[string]string)
	prevRestarts := make(map[string]int)
	var prevConflicts int
	var prevFailed []string

	if prev != nil {
		for _, s := range prev.Services {
			prevSvc[s.Name] = s.Status
		}
		for _, d := range prev.Docker {
			prevDocker[d.Name] = d.Status
			prevRestarts[d.Name] = d.Restarts
		}
		prevConflicts = prev.Sync.Conflicts
		prevFailed = prev.FailedUnits
	}

	for _, s := range current.Services {
		old := prevSvc[s.Name]
		if old != s.Status {
			gw.ticker.Push("svc", levelForStatus(s.Status), s.Name+" "+s.Status, s.Detail)
		}
	}

	for _, d := range current.Docker {
		old := prevDocker[d.Name]
		if old != d.Status {
			gw.ticker.Push("docker", levelForStatus(d.Status), d.Name+" "+d.Status, "")
		}
		if d.Restarts > prevRestarts[d.Name] {
			gw.ticker.Push("docker", "warn", d.Name+" restarted", fmt.Sprintf("count: %d", d.Restarts))
		}
	}

	if current.Sync.Conflicts > 0 && current.Sync.Conflicts != prevConflicts {
		gw.ticker.Push("sync", "warn", fmt.Sprintf("%d sync conflicts", current.Sync.Conflicts), "")
	}

	if len(current.FailedUnits) > 0 && !stringSliceEqual(current.FailedUnits, prevFailed) {
		gw.ticker.Push("svc", "error", fmt.Sprintf("%d failed units", len(current.FailedUnits)), strings.Join(current.FailedUnits, ", "))
	}
}

func levelForStatus(status string) string {
	switch status {
	case "up", "running", "online":
		return "info"
	case "degraded", "unhealthy":
		return "warn"
	default:
		return "error"
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func checkHTTP(c HTTPCheck) ServiceEntry {
	entry := ServiceEntry{Name: c.Name, Port: c.Port, Status: "down"}
	start := time.Now()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", c.URL, nil)
	if err != nil {
		entry.Detail = "bad url"
		return entry
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	entry.Latency = int(time.Since(start).Milliseconds())
	if err != nil {
		entry.Detail = "unreachable"
		return entry
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		entry.Status = "up"
	} else if resp.StatusCode < 500 {
		entry.Status = "degraded"
		entry.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	} else {
		entry.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return entry
}

func checkTunnel(t TunnelCheck) TunnelEntry {
	entry := TunnelEntry{Name: t.Name, Hostname: t.Hostname, Status: "down"}
	start := time.Now()
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://" + t.Hostname)
	entry.Latency = int(time.Since(start).Milliseconds())
	if err != nil {
		return entry
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	// Any response means tunnel is routing.
	entry.Status = "up"
	return entry
}

func checkDocker(host string) []DockerEntry {
	if host == "" {
		return nil
	}

	// Use docker inspect format to get everything in one call.
	format := `{{.Name}}|{{.State.Status}}|{{.State.StartedAt}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{range $k,$v := .NetworkSettings.Ports}}{{$k}} {{end}}|{{.RestartCount}}`
	var cmd *exec.Cmd
	if host == "local" || host == "" {
		cmd = exec.Command("docker", "ps", "-q")
	} else {
		cmd = exec.Command("ssh", "-o", "ConnectTimeout=4", "-o", "BatchMode=yes", host,
			"docker ps -q")
	}
	idsOut, err := cmd.Output()
	if err != nil || len(bytes.TrimSpace(idsOut)) == 0 {
		return nil
	}

	ids := strings.Fields(strings.TrimSpace(string(idsOut)))
	args := append([]string{"inspect", "--format", format}, ids...)

	var inspectCmd *exec.Cmd
	if host == "local" {
		inspectCmd = exec.Command("docker", args...)
	} else {
		// Build the remote command with proper quoting.
		remoteArgs := "docker inspect --format '" + format + "' " + strings.Join(ids, " ")
		inspectCmd = exec.Command("ssh", "-o", "ConnectTimeout=4", "-o", "BatchMode=yes", host, remoteArgs)
	}

	out, err := inspectCmd.Output()
	if err != nil {
		return nil
	}

	var entries []DockerEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 6)
		if len(parts) < 4 {
			continue
		}
		name := strings.TrimPrefix(parts[0], "/")
		state := parts[1]
		startedAt := parts[2]
		health := parts[3]

		status := state
		if health == "unhealthy" {
			status = "unhealthy"
		}

		// Calculate uptime.
		uptime := ""
		if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
			dur := time.Since(t)
			if dur.Hours() >= 24 {
				uptime = fmt.Sprintf("%dd", int(dur.Hours()/24))
			} else if dur.Hours() >= 1 {
				uptime = fmt.Sprintf("%dh", int(dur.Hours()))
			} else {
				uptime = fmt.Sprintf("%dm", int(dur.Minutes()))
			}
		}

		// Extract first port.
		port := ""
		if len(parts) > 4 {
			portParts := strings.Fields(parts[4])
			if len(portParts) > 0 {
				port = strings.Split(portParts[0], "/")[0]
			}
		}

		// Parse restart count.
		restarts := 0
		if len(parts) > 5 {
			restarts, _ = strconv.Atoi(strings.TrimSpace(parts[5]))
		}

		entries = append(entries, DockerEntry{
			Name:     name,
			Status:   status,
			Health:   health,
			Uptime:   uptime,
			Port:     port,
			Restarts: restarts,
		})
	}
	return entries
}

func checkSyncthing(url, key string, folders []string) SyncStatus {
	if url == "" || key == "" {
		return SyncStatus{}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	ss := SyncStatus{}

	// Check connections.
	req, _ := http.NewRequest("GET", url+"/rest/system/connections", nil)
	req.Header.Set("X-API-Key", key)
	if resp, err := client.Do(req); err == nil {
		var conns struct {
			Connections map[string]struct {
				Connected bool `json:"connected"`
			} `json:"connections"`
		}
		json.NewDecoder(resp.Body).Decode(&conns)
		resp.Body.Close()
		for _, c := range conns.Connections {
			if c.Connected {
				ss.Connected = true
				break
			}
		}
	}

	// Check each folder.
	for _, folder := range folders {
		req, _ := http.NewRequest("GET", url+"/rest/db/status?folder="+folder, nil)
		req.Header.Set("X-API-Key", key)
		if resp, err := client.Do(req); err == nil {
			var fs struct {
				State        string `json:"state"`
				GlobalFiles  int    `json:"globalFiles"`
				NeedFiles    int    `json:"needFiles"`
				GlobalBytes  int64  `json:"globalBytes"`
				StateChanged string `json:"stateChanged"`
			}
			json.NewDecoder(resp.Body).Decode(&fs)
			resp.Body.Close()
			ss.Folders = append(ss.Folders, SyncFolder{
				ID:        folder,
				State:     fs.State,
				Files:     fs.GlobalFiles,
				NeedFiles: fs.NeedFiles,
				SizeGB:    float64(fs.GlobalBytes) / 1024 / 1024 / 1024,
				LastScan:  fs.StateChanged,
			})
		}
	}
	return ss
}

// checkSyncthingSSH runs syncthing API calls via SSH when the API is localhost-only.
func checkSyncthingSSH(sshHost, url, key string, folders []string) SyncStatus {
	ss := SyncStatus{}

	// Build a script that curls the syncthing API locally and counts conflict files.
	var script strings.Builder
	script.WriteString(fmt.Sprintf(`curl -s -H "X-API-Key: %s" "%s/rest/system/connections" 2>/dev/null; echo "|||"`, key, url))
	for _, f := range folders {
		script.WriteString(fmt.Sprintf(`; curl -s -H "X-API-Key: %s" "%s/rest/db/status?folder=%s" 2>/dev/null; echo "|||"`, key, url, f))
	}
	// Append conflict file count as the last section.
	script.WriteString(`; find ~/projects ~/hfl-projects -name "*.sync-conflict-*" 2>/dev/null | wc -l; echo "|||"`)

	cmd := exec.Command("ssh", "-o", "ConnectTimeout=4", "-o", "BatchMode=yes", sshHost, "bash -c "+shellQuote(script.String()))
	out, err := cmd.Output()
	if err != nil {
		return ss
	}

	parts := strings.Split(string(out), "|||")

	// Parse connections.
	if len(parts) > 0 {
		var conns struct {
			Connections map[string]struct {
				Connected bool `json:"connected"`
			} `json:"connections"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(parts[0])), &conns) == nil {
			for _, c := range conns.Connections {
				if c.Connected {
					ss.Connected = true
					break
				}
			}
		}
	}

	// Parse folder statuses.
	for i, folder := range folders {
		if i+1 >= len(parts) {
			break
		}
		var fs struct {
			State        string `json:"state"`
			GlobalFiles  int    `json:"globalFiles"`
			NeedFiles    int    `json:"needFiles"`
			GlobalBytes  int64  `json:"globalBytes"`
			StateChanged string `json:"stateChanged"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(parts[i+1])), &fs) == nil {
			ss.Folders = append(ss.Folders, SyncFolder{
				ID:        folder,
				State:     fs.State,
				Files:     fs.GlobalFiles,
				NeedFiles: fs.NeedFiles,
				SizeGB:    float64(fs.GlobalBytes) / 1024 / 1024 / 1024,
				LastScan:  fs.StateChanged,
			})
		}
	}

	// Last section is the conflict file count.
	conflictIdx := len(folders) + 1
	if conflictIdx < len(parts) {
		ss.Conflicts, _ = strconv.Atoi(strings.TrimSpace(parts[conflictIdx]))
	}
	return ss
}

func checkChezmoi(host string) ChezmoiStatus {
	cs := ChezmoiStatus{}
	if host == "" {
		return cs
	}

	// Get last commit time and diff counts.
	cmd := `git -C ~/.local/share/chezmoi log --oneline -1 --format="%s" 2>/dev/null; echo "---"; chezmoi status 2>/dev/null | wc -l; chezmoi status 2>/dev/null | grep "^MM" | wc -l; chezmoi status 2>/dev/null | grep "^DA" | wc -l`

	var c *exec.Cmd
	if host == "local" {
		c = exec.Command("bash", "-c", cmd)
	} else {
		c = exec.Command("ssh", "-o", "ConnectTimeout=4", "-o", "BatchMode=yes", host, "bash -c "+shellQuote(cmd))
	}

	out, err := c.Output()
	if err != nil {
		return cs
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) >= 1 {
		cs.LastCommit = lines[0]
	}
	if len(lines) >= 4 {
		// lines[1] = "---"
		cs.Modified, _ = strconv.Atoi(strings.TrimSpace(lines[2]))
		cs.Added, _ = strconv.Atoi(strings.TrimSpace(lines[3]))
	}
	return cs
}

// checkFailedUnits lists failed systemd --user units on the given host.
func checkFailedUnits(host string) []string {
	if host == "" {
		return nil
	}
	cmd := `systemctl --user list-units --state=failed --no-legend --no-pager 2>/dev/null | awk '{print $1}'`
	var c *exec.Cmd
	if host == "local" {
		c = exec.Command("bash", "-c", cmd)
	} else {
		c = exec.Command("ssh", "-o", "ConnectTimeout=4", "-o", "BatchMode=yes", host, "bash -c "+shellQuote(cmd))
	}
	out, err := c.Output()
	if err != nil {
		return nil
	}
	var units []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			units = append(units, line)
		}
	}
	return units
}
