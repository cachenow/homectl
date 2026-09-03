package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"homectl/internal/protocol"
)

func writeTelemetryFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMetricPolicyDefaultsAndRejectsInvalidUpdates(t *testing.T) {
	a := &agent{}
	if !a.setMetricCards(nil) {
		t.Fatal("default metric policy was rejected")
	}
	if got := a.metricCardsSnapshot(); len(got) != len(protocol.DefaultMetricCards()) {
		t.Fatalf("default metric cards=%v", got)
	}
	if a.setMetricCards([]string{protocol.MetricCPU, protocol.MetricMemory}) {
		t.Fatal("policy with fewer than three cards was accepted")
	}
	if got := a.metricCardsSnapshot(); len(got) != len(protocol.DefaultMetricCards()) {
		t.Fatalf("invalid update changed active policy=%v", got)
	}
}

func TestProcessScanParsesComplexNamesAndCachesForSixtySeconds(t *testing.T) {
	root := t.TempDir()
	writeTelemetryFixture(t, filepath.Join(root, "101", "stat"), "101 (worker) helper) R 1 2 3\n")
	writeTelemetryFixture(t, filepath.Join(root, "102", "stat"), "102 (sleep) S 1 2 3\n")
	writeTelemetryFixture(t, filepath.Join(root, "103", "stat"), "103 (zombie) Z 1 2 3\n")
	writeTelemetryFixture(t, filepath.Join(root, "not-a-pid", "stat"), "ignored")

	var state telemetryState
	start := time.Unix(100, 0)
	first := state.processes(start, root)
	if first.Total != 3 || first.Running != 1 || first.Sleeping != 1 || first.Zombie != 1 {
		t.Fatalf("process metrics=%#v", first)
	}
	writeTelemetryFixture(t, filepath.Join(root, "103", "stat"), "103 (reaped) S 1 2 3\n")
	if cached := state.processes(start.Add(59*time.Second), root); cached.Zombie != 1 {
		t.Fatalf("process cache refreshed too early: %#v", cached)
	}
	if refreshed := state.processes(start.Add(61*time.Second), root); refreshed.Zombie != 0 || refreshed.Sleeping != 2 {
		t.Fatalf("process cache did not refresh: %#v", refreshed)
	}
}

func TestNetworkSamplingUsesDefaultRouteAndFiveMinutePeak(t *testing.T) {
	root := t.TempDir()
	route := filepath.Join(root, "route")
	dev := filepath.Join(root, "dev")
	sysNet := filepath.Join(root, "sys-net")
	writeTelemetryFixture(t, route, "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\neth0 00000000 00000000 0003 0 0 10 00000000 0 0 0\n")
	writeTelemetryFixture(t, dev, "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\neth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n")
	writeTelemetryFixture(t, filepath.Join(sysNet, "eth0", "speed"), "1000\n")

	var state telemetryState
	start := time.Unix(100, 0)
	first := state.network(start, route, dev, sysNet)
	if first.Interface != "eth0" || first.LinkBPS != 1_000_000_000 || first.RXBPS != 0 {
		t.Fatalf("first network sample=%#v", first)
	}
	writeTelemetryFixture(t, dev, "eth0: 3000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n")
	second := state.network(start.Add(10*time.Second), route, dev, sysNet)
	if second.RXBPS != 200 || second.TXBPS != 300 || second.RXPeakBPS != 200 || second.TXPeakBPS != 300 {
		t.Fatalf("second network sample=%#v", second)
	}
	writeTelemetryFixture(t, dev, "eth0: 4000 0 0 0 0 0 0 0 6000 0 0 0 0 0 0 0\n")
	third := state.network(start.Add(6*time.Minute), route, dev, sysNet)
	if third.RXPeakBPS >= 200 || third.TXPeakBPS >= 300 {
		t.Fatalf("expired five-minute peak was retained: %#v", third)
	}
}

func TestDiskIOSamplingCalculatesRatesWaitAndLatency(t *testing.T) {
	root := t.TempDir()
	diskstats := filepath.Join(root, "diskstats")
	stat := filepath.Join(root, "stat")
	sysBlock := filepath.Join(root, "block")
	if err := os.MkdirAll(filepath.Join(sysBlock, "sda", "holders"), 0755); err != nil {
		t.Fatal(err)
	}
	writeTelemetryFixture(t, diskstats, "8 0 sda 10 0 100 20 20 0 200 40 0 60 70\n")
	writeTelemetryFixture(t, stat, "cpu 100 0 50 500 10 0 0 0 1000 2000\n")

	var state telemetryState
	start := time.Unix(100, 0)
	if first := state.diskIO(start, diskstats, stat, sysBlock); first.IOPS != 0 {
		t.Fatalf("first disk sample should only prime counters: %#v", first)
	}
	writeTelemetryFixture(t, diskstats, "8 0 sda 20 0 300 40 40 0 500 80 0 120 140\n")
	writeTelemetryFixture(t, stat, "cpu 200 0 100 900 30 0 0 0 5000 9000\n")
	second := state.diskIO(start.Add(10*time.Second), diskstats, stat, sysBlock)
	if second.ReadBPS != 10240 || second.WriteBPS != 15360 || second.IOPS != 3 || second.LatencyMS != 2 {
		t.Fatalf("disk rates=%#v", second)
	}
	if math.Abs(second.IOWaitPct-(20.0/570.0*100)) > 0.001 {
		t.Fatalf("iowait=%f", second.IOWaitPct)
	}
}

func TestCPUTemperaturePrefersCPUZoneAndHidesMissing(t *testing.T) {
	root := t.TempDir()
	thermal := filepath.Join(root, "thermal")
	hwmon := filepath.Join(root, "hwmon")
	if value := readCPUTemperature(thermal, hwmon); value != nil {
		t.Fatalf("missing temperature=%v", *value)
	}
	writeTelemetryFixture(t, filepath.Join(thermal, "thermal_zone0", "type"), "battery\n")
	writeTelemetryFixture(t, filepath.Join(thermal, "thermal_zone0", "temp"), "31000\n")
	writeTelemetryFixture(t, filepath.Join(thermal, "thermal_zone1", "type"), "x86_pkg_temp\n")
	writeTelemetryFixture(t, filepath.Join(thermal, "thermal_zone1", "temp"), "47500\n")
	value := readCPUTemperature(thermal, hwmon)
	if value == nil || *value != 47.5 {
		t.Fatalf("cpu temperature=%v", value)
	}
}

func TestCPUTemperatureCacheRefreshesAfterSixtySeconds(t *testing.T) {
	root := t.TempDir()
	thermal := filepath.Join(root, "thermal")
	hwmon := filepath.Join(root, "hwmon")
	temperaturePath := filepath.Join(thermal, "thermal_zone0", "temp")
	writeTelemetryFixture(t, filepath.Join(thermal, "thermal_zone0", "type"), "cpu-thermal\n")
	writeTelemetryFixture(t, temperaturePath, "47500\n")

	var state telemetryState
	start := time.Unix(100, 0)
	first := state.cpuTemperature(start, thermal, hwmon)
	if first == nil || *first != 47.5 {
		t.Fatalf("first cached temperature=%v", first)
	}
	writeTelemetryFixture(t, temperaturePath, "60000\n")
	if cached := state.cpuTemperature(start.Add(59*time.Second), thermal, hwmon); cached == nil || *cached != 47.5 {
		t.Fatalf("temperature cache refreshed too early: %v", cached)
	}
	if refreshed := state.cpuTemperature(start.Add(61*time.Second), thermal, hwmon); refreshed == nil || *refreshed != 60 {
		t.Fatalf("temperature cache did not refresh: %v", refreshed)
	}
}
