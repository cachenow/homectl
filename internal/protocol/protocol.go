package protocol

import "encoding/json"

const (
	EnrollmentTokenLength         = 48
	DeviceTokenLength             = 64
	CapabilityFileDownloadCredits = "file-download-credits-v1"
	CapabilityFileUploadCredits   = "file-upload-credits-v1"
)

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
	CPUUsage          float64  `json:"cpu_usage"`
	Load1             string   `json:"load1"`
	MemTotal          uint64   `json:"mem_total"`
	MemAvail          uint64   `json:"mem_available"`
	DiskTotal         uint64   `json:"disk_total"`
	DiskFree          uint64   `json:"disk_free"`
	DiskPhysicalTotal uint64   `json:"disk_physical_total"`
	DiskPhysicalCount int      `json:"disk_physical_count"`
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
