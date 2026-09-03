package protocol

import (
	"encoding/json"
	"errors"
)

const (
	EnrollmentTokenLength         = 48
	DeviceTokenLength             = 64
	CapabilityFileDownloadCredits = "file-download-credits-v1"
	CapabilityFileUploadCredits   = "file-upload-credits-v1"
	CapabilityMetricPolicy        = "metric-policy-v1"
)

const (
	MetricCPU       = "cpu"
	MetricMemory    = "memory"
	MetricDisk      = "disk"
	MetricNetwork   = "network"
	MetricProcesses = "processes"
	MetricDiskIO    = "diskio"
)

var metricCardOrder = [...]string{
	MetricCPU,
	MetricMemory,
	MetricDisk,
	MetricNetwork,
	MetricProcesses,
	MetricDiskIO,
}

type Message struct {
	Type            string      `json:"type"`
	DeviceID        string      `json:"device_id,omitempty"`
	Name            string      `json:"name,omitempty"`
	Token           string      `json:"token,omitempty"`
	EnrollmentToken string      `json:"enrollment_token,omitempty"`
	DeviceToken     string      `json:"device_token,omitempty"`
	Capabilities    []string    `json:"capabilities,omitempty"`
	RequestID       string      `json:"request_id,omitempty"`
	SessionID       string      `json:"session_id,omitempty"`
	Action          string      `json:"action,omitempty"`
	Command         string      `json:"command,omitempty"`
	Path            string      `json:"path,omitempty"`
	Target          string      `json:"target,omitempty"`
	Data            string      `json:"data,omitempty"`
	Error           string      `json:"error,omitempty"`
	ExitCode        int         `json:"exit_code,omitempty"`
	Cols            uint16      `json:"cols,omitempty"`
	Rows            uint16      `json:"rows,omitempty"`
	Size            int64       `json:"size,omitempty"`
	Credits         int         `json:"credits,omitempty"`
	Mode            uint32      `json:"mode,omitempty"`
	Entries         []FileEntry `json:"entries,omitempty"`
	Info            *SystemInfo `json:"info,omitempty"`
	MetricCards     []string    `json:"metric_cards,omitempty"`
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
	Hostname          string   `json:"hostname"`
	OS                string   `json:"os"`
	Kernel            string   `json:"kernel"`
	Arch              string   `json:"arch"`
	CPUModel          string   `json:"cpu_model"`
	CPUCores          int      `json:"cpu_cores"`
	CPUUsage          float64  `json:"cpu_usage,omitempty"`
	Load1             string   `json:"load1,omitempty"`
	CPUTempC          *float64 `json:"cpu_temp_c,omitempty"`
	MemTotal          uint64   `json:"mem_total,omitempty"`
	MemAvail          uint64   `json:"mem_available,omitempty"`
	DiskTotal         uint64   `json:"disk_total,omitempty"`
	DiskFree          uint64   `json:"disk_free,omitempty"`
	DiskPhysicalTotal uint64   `json:"disk_physical_total,omitempty"`
	DiskPhysicalCount int      `json:"disk_physical_count,omitempty"`
	NetInterface      string   `json:"net_interface,omitempty"`
	NetLinkBPS        uint64   `json:"net_link_bps,omitempty"`
	NetRXBPS          float64  `json:"net_rx_bps,omitempty"`
	NetTXBPS          float64  `json:"net_tx_bps,omitempty"`
	NetRXPeak5mBPS    float64  `json:"net_rx_peak_5m_bps,omitempty"`
	NetTXPeak5mBPS    float64  `json:"net_tx_peak_5m_bps,omitempty"`
	ProcessTotal      int      `json:"process_total,omitempty"`
	ProcessRunning    int      `json:"process_running,omitempty"`
	ProcessSleeping   int      `json:"process_sleeping,omitempty"`
	ProcessZombie     int      `json:"process_zombie,omitempty"`
	DiskReadBPS       float64  `json:"disk_read_bps,omitempty"`
	DiskWriteBPS      float64  `json:"disk_write_bps,omitempty"`
	DiskIOPS          float64  `json:"disk_iops,omitempty"`
	DiskIOWaitPct     float64  `json:"disk_io_wait_pct,omitempty"`
	DiskLatencyMS     float64  `json:"disk_latency_ms,omitempty"`
	UptimeSec         uint64   `json:"uptime_sec"`
	IPAddrs           []string `json:"ip_addrs"`
	AgentVer          string   `json:"agent_version"`
	ReportedAt        int64    `json:"reported_at"`
}

func MarshalSystemInfo(v *SystemInfo) ([]byte, error)   { return json.Marshal(v) }
func UnmarshalSystemInfo(b []byte, v *SystemInfo) error { return json.Unmarshal(b, v) }

func ValidEnrollmentToken(token string) bool {
	return validLowerHexToken(token, EnrollmentTokenLength)
}

func ValidDeviceToken(token string) bool {
	return validLowerHexToken(token, DeviceTokenLength)
}

func validLowerHexToken(token string, length int) bool {
	if len(token) != length {
		return false
	}
	for _, ch := range token {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func HasCapability(capabilities []string, capability string) bool {
	for _, value := range capabilities {
		if value == capability {
			return true
		}
	}
	return false
}

func DefaultMetricCards() []string {
	return append([]string(nil), metricCardOrder[:]...)
}

func NormalizeMetricCards(cards []string, minimum int) ([]string, error) {
	if minimum < 0 || minimum > len(metricCardOrder) {
		return nil, errors.New("invalid metric-card minimum")
	}
	seen := make(map[string]bool, len(cards))
	for _, card := range cards {
		if seen[card] {
			return nil, errors.New("metric cards must not contain duplicates")
		}
		valid := false
		for _, allowed := range metricCardOrder {
			if card == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return nil, errors.New("unknown metric card")
		}
		seen[card] = true
	}
	if len(seen) < minimum {
		return nil, errors.New("at least three metric cards are required")
	}
	out := make([]string, 0, len(seen))
	for _, card := range metricCardOrder {
		if seen[card] {
			out = append(out, card)
		}
	}
	return out, nil
}

func HasMetricCard(cards []string, card string) bool {
	for _, value := range cards {
		if value == card {
			return true
		}
	}
	return false
}

// ClearDynamicMetrics retains inventory fields while removing values which
// would otherwise make an offline device look active in the dashboard API.
func ClearDynamicMetrics(info *SystemInfo) *SystemInfo {
	if info == nil {
		return nil
	}
	out := *info
	out.CPUUsage = 0
	out.Load1 = ""
	out.CPUTempC = nil
	out.MemTotal = 0
	out.MemAvail = 0
	out.DiskTotal = 0
	out.DiskFree = 0
	out.DiskPhysicalTotal = 0
	out.DiskPhysicalCount = 0
	out.NetInterface = ""
	out.NetLinkBPS = 0
	out.NetRXBPS = 0
	out.NetTXBPS = 0
	out.NetRXPeak5mBPS = 0
	out.NetTXPeak5mBPS = 0
	out.ProcessTotal = 0
	out.ProcessRunning = 0
	out.ProcessSleeping = 0
	out.ProcessZombie = 0
	out.DiskReadBPS = 0
	out.DiskWriteBPS = 0
	out.DiskIOPS = 0
	out.DiskIOWaitPct = 0
	out.DiskLatencyMS = 0
	return &out
}
