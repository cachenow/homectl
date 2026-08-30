package protocol

import "encoding/json"

type Message struct {
	Type        string      `json:"type"`
	DeviceID    string      `json:"device_id,omitempty"`
	Name        string      `json:"name,omitempty"`
	Token       string      `json:"token,omitempty"`
	DeviceToken string      `json:"device_token,omitempty"`
	RequestID   string      `json:"request_id,omitempty"`
	SessionID   string      `json:"session_id,omitempty"`
	Action      string      `json:"action,omitempty"`
	Command     string      `json:"command,omitempty"`
	Path        string      `json:"path,omitempty"`
	Target      string      `json:"target,omitempty"`
	Data        string      `json:"data,omitempty"`
	Error       string      `json:"error,omitempty"`
	ExitCode    int         `json:"exit_code,omitempty"`
	Cols        uint16      `json:"cols,omitempty"`
	Rows        uint16      `json:"rows,omitempty"`
	Size        int64       `json:"size,omitempty"`
	Mode        uint32      `json:"mode,omitempty"`
	Entries     []FileEntry `json:"entries,omitempty"`
	Info        *SystemInfo `json:"info,omitempty"`
}

type FileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	ModTime   int64  `json:"mod_time"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink"`
}

type SystemInfo struct {
	Hostname   string   `json:"hostname"`
	OS         string   `json:"os"`
	Kernel     string   `json:"kernel"`
	Arch       string   `json:"arch"`
	CPUModel   string   `json:"cpu_model"`
	CPUCores   int      `json:"cpu_cores"`
	CPUUsage   float64  `json:"cpu_usage"`
	Load1      string   `json:"load1"`
	MemTotal   uint64   `json:"mem_total"`
	MemAvail   uint64   `json:"mem_available"`
	DiskTotal  uint64   `json:"disk_total"`
	DiskFree   uint64   `json:"disk_free"`
	UptimeSec  uint64   `json:"uptime_sec"`
	IPAddrs    []string `json:"ip_addrs"`
	AgentVer   string   `json:"agent_version"`
	ReportedAt int64    `json:"reported_at"`
}

func MarshalSystemInfo(v *SystemInfo) ([]byte, error)   { return json.Marshal(v) }
func UnmarshalSystemInfo(b []byte, v *SystemInfo) error { return json.Unmarshal(b, v) }
