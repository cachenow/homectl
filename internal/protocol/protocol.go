package protocol

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
	Data        string      `json:"data,omitempty"`
	Error       string      `json:"error,omitempty"`
	Cols        uint16      `json:"cols,omitempty"`
	Rows        uint16      `json:"rows,omitempty"`
	Info        *SystemInfo `json:"info,omitempty"`
}

type SystemInfo struct {
	Hostname   string   `json:"hostname"`
	OS         string   `json:"os"`
	Kernel     string   `json:"kernel"`
	Arch       string   `json:"arch"`
	CPUModel   string   `json:"cpu_model"`
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
