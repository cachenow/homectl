package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenFormats(t *testing.T) {
	validEnrollment := "0123456789abcdef0123456789abcdef0123456789abcdef"
	validDevice := validEnrollment + "0123456789abcdef"
	if !ValidEnrollmentToken(validEnrollment) {
		t.Fatal("valid 48-character enrollment token was rejected")
	}
	if !ValidDeviceToken(validDevice) {
		t.Fatal("valid 64-character device token was rejected")
	}
	for _, token := range []string{
		validEnrollment[:len(validEnrollment)-1],
		validEnrollment + "0",
		"0123456789ABCDEF0123456789abcdef0123456789abcdef",
		"g123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if ValidEnrollmentToken(token) {
			t.Fatalf("malformed enrollment token was accepted: %q", token)
		}
	}
}

func TestHasCapability(t *testing.T) {
	capabilities := []string{"other", CapabilityFileDownloadCredits, CapabilityFileUploadCredits}
	if !HasCapability(capabilities, CapabilityFileDownloadCredits) {
		t.Fatal("declared capability was not found")
	}
	if !HasCapability(capabilities, CapabilityFileUploadCredits) {
		t.Fatal("upload credit capability was not found")
	}
	if HasCapability([]string{"other"}, CapabilityFileDownloadCredits) {
		t.Fatal("missing capability was reported")
	}
}

func TestMetricCardsNormalizeAndEnforceMinimum(t *testing.T) {
	cards, err := NormalizeMetricCards([]string{MetricDiskIO, MetricCPU, MetricMemory}, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{MetricCPU, MetricMemory, MetricDiskIO}
	for index := range want {
		if cards[index] != want[index] {
			t.Fatalf("normalized cards=%v want=%v", cards, want)
		}
	}
	for _, invalid := range [][]string{
		{MetricCPU, MetricMemory},
		{MetricCPU, MetricCPU, MetricMemory},
		{MetricCPU, MetricMemory, "unknown"},
	} {
		if _, err := NormalizeMetricCards(invalid, 3); err == nil {
			t.Fatalf("invalid metric cards were accepted: %v", invalid)
		}
	}
}

func TestClearDynamicMetricsRetainsInventory(t *testing.T) {
	temperature := 47.5
	info := &SystemInfo{
		Hostname: "HomeServer", OS: "Linux", CPUModel: "CPU", CPUCores: 8,
		CPUUsage: 25, CPUTempC: &temperature, MemTotal: 100, DiskTotal: 200,
		NetRXBPS: 300, ProcessZombie: 1, DiskIOPS: 20, UptimeSec: 99,
		IPAddrs: []string{"192.0.2.1"}, AgentVer: "v2.0.0",
	}
	cleared := ClearDynamicMetrics(info)
	if cleared == info {
		t.Fatal("dynamic clear mutated the stored object in place")
	}
	if cleared.Hostname != info.Hostname || cleared.CPUModel != info.CPUModel || cleared.UptimeSec != info.UptimeSec || cleared.AgentVer != info.AgentVer {
		t.Fatalf("inventory fields changed: %#v", cleared)
	}
	if cleared.CPUUsage != 0 || cleared.CPUTempC != nil || cleared.MemTotal != 0 || cleared.DiskTotal != 0 || cleared.NetRXBPS != 0 || cleared.ProcessZombie != 0 || cleared.DiskIOPS != 0 {
		t.Fatalf("dynamic fields remain: %#v", cleared)
	}
	if info.CPUUsage != 25 || info.CPUTempC == nil {
		t.Fatal("original info was modified")
	}
}

func TestZeroDynamicMetricsAreOmittedFromHeartbeatJSON(t *testing.T) {
	encoded, err := json.Marshal(SystemInfo{Hostname: "host", CPUCores: 4, AgentVer: "v2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, field := range []string{"cpu_usage", "mem_total", "disk_total", "net_rx_bps", "process_total", "disk_iops"} {
		if strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("zero dynamic field %q was transmitted: %s", field, text)
		}
	}
}
