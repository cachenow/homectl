package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homectl/internal/protocol"
)

const (
	processCacheTTL       = 60 * time.Second
	temperatureCacheTTL   = 60 * time.Second
	networkPeakWindow     = 5 * time.Minute
	maxNetworkPeakSamples = 600
)

type telemetryState struct {
	mu sync.Mutex

	networkPrimed bool
	networkPrev   networkCounterSample
	networkPeaks  []networkPeakSample

	diskIOPrimed bool
	diskIOPrev   diskCounterSample

	processCachedAt time.Time
	processCache    processMetrics

	temperatureCachedAt time.Time
	temperatureCache    *float64
}

type networkCounterSample struct {
	At        time.Time
	Interface string
	RXBytes   uint64
	TXBytes   uint64
}

type networkPeakSample struct {
	At    time.Time
	RXBPS float64
	TXBPS float64
}

type networkMetrics struct {
	Interface string
	LinkBPS   uint64
	RXBPS     float64
	TXBPS     float64
	RXPeakBPS float64
	TXPeakBPS float64
}

type processMetrics struct {
	Total    int
	Running  int
	Sleeping int
	Zombie   int
}

type diskCounters struct {
	ReadBytes  uint64
	WriteBytes uint64
	Operations uint64
	ServiceMS  uint64
	CPUTotal   uint64
	CPUIOWait  uint64
}

type diskCounterSample struct {
	At       time.Time
	Counters diskCounters
}

type diskIOMetrics struct {
	ReadBPS   float64
	WriteBPS  float64
	IOPS      float64
	IOWaitPct float64
	LatencyMS float64
}

func (a *agent) setMetricCards(cards []string) bool {
	if len(cards) == 0 {
		cards = protocol.DefaultMetricCards()
	}
	normalized, err := protocol.NormalizeMetricCards(cards, 3)
	if err != nil {
		return false
	}
	a.policyMu.Lock()
	old := append([]string(nil), a.metricCards...)
	a.metricCards = normalized
	a.policyMu.Unlock()

	if protocol.HasMetricCard(old, protocol.MetricCPU) != protocol.HasMetricCard(normalized, protocol.MetricCPU) {
		a.cpuMu.Lock()
		a.cpuPrimed = false
		a.prevCPUTotal = 0
		a.prevCPUIdle = 0
		a.cpuMu.Unlock()
	}
	a.telemetry.resetChangedCollectors(old, normalized)
	return true
}

func (a *agent) metricCardsSnapshot() []string {
	a.policyMu.RLock()
	defer a.policyMu.RUnlock()
	if len(a.metricCards) == 0 {
		return protocol.DefaultMetricCards()
	}
	return append([]string(nil), a.metricCards...)
}

func (t *telemetryState) resetChangedCollectors(old, current []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if protocol.HasMetricCard(old, protocol.MetricNetwork) != protocol.HasMetricCard(current, protocol.MetricNetwork) {
		t.networkPrimed = false
		t.networkPrev = networkCounterSample{}
		t.networkPeaks = nil
	}
	if protocol.HasMetricCard(old, protocol.MetricDiskIO) != protocol.HasMetricCard(current, protocol.MetricDiskIO) {
		t.diskIOPrimed = false
		t.diskIOPrev = diskCounterSample{}
	}
	if !protocol.HasMetricCard(current, protocol.MetricProcesses) {
		t.processCachedAt = time.Time{}
		t.processCache = processMetrics{}
	}
	if protocol.HasMetricCard(old, protocol.MetricCPU) != protocol.HasMetricCard(current, protocol.MetricCPU) {
		t.temperatureCachedAt = time.Time{}
		t.temperatureCache = nil
	}
}

func (t *telemetryState) network(now time.Time, routePath, devPath, sysNetRoot string) networkMetrics {
	preferred := defaultRouteInterface(routePath)
	iface, rx, tx, ok := networkCounters(devPath, preferred)
	if !ok {
		return networkMetrics{}
	}
	result := networkMetrics{Interface: iface, LinkBPS: networkLinkBPS(sysNetRoot, iface)}

	t.mu.Lock()
	defer t.mu.Unlock()
	current := networkCounterSample{At: now, Interface: iface, RXBytes: rx, TXBytes: tx}
	if t.networkPrimed && t.networkPrev.Interface == iface {
		seconds := now.Sub(t.networkPrev.At).Seconds()
		if seconds > 0 && rx >= t.networkPrev.RXBytes && tx >= t.networkPrev.TXBytes {
			result.RXBPS = float64(rx-t.networkPrev.RXBytes) / seconds
			result.TXBPS = float64(tx-t.networkPrev.TXBytes) / seconds
			t.networkPeaks = append(t.networkPeaks, networkPeakSample{At: now, RXBPS: result.RXBPS, TXBPS: result.TXBPS})
		}
	} else {
		t.networkPeaks = nil
	}
	t.networkPrev = current
	t.networkPrimed = true

	cutoff := now.Add(-networkPeakWindow)
	first := 0
	for first < len(t.networkPeaks) && t.networkPeaks[first].At.Before(cutoff) {
		first++
	}
	if first > 0 {
		t.networkPeaks = append([]networkPeakSample(nil), t.networkPeaks[first:]...)
	}
	if len(t.networkPeaks) > maxNetworkPeakSamples {
		t.networkPeaks = append([]networkPeakSample(nil), t.networkPeaks[len(t.networkPeaks)-maxNetworkPeakSamples:]...)
	}
	for _, sample := range t.networkPeaks {
		if sample.RXBPS > result.RXPeakBPS {
			result.RXPeakBPS = sample.RXBPS
		}
		if sample.TXBPS > result.TXPeakBPS {
			result.TXPeakBPS = sample.TXBPS
		}
	}
	return result
}

func defaultRouteInterface(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	bestMetric := int64(^uint64(0) >> 1)
	best := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		metric, err := strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			metric = bestMetric
		}
		if best == "" || metric < bestMetric {
			best, bestMetric = fields[0], metric
		}
	}
	return best
}

func networkCounters(path, preferred string) (string, uint64, uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, false
	}
	defer f.Close()
	type counters struct{ rx, tx uint64 }
	all := make(map[string]counters)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if name == "" || name == "lo" || len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr == nil && txErr == nil {
			all[name] = counters{rx: rx, tx: tx}
		}
	}
	if value, ok := all[preferred]; ok {
		return preferred, value.rx, value.tx, true
	}
	if len(all) == 0 {
		return "", 0, 0, false
	}
	bestName := ""
	var bestActivity uint64
	for name, value := range all {
		activity := value.rx + value.tx
		if activity < value.rx {
			activity = ^uint64(0)
		}
		if bestName == "" || activity > bestActivity || activity == bestActivity && name < bestName {
			bestName, bestActivity = name, activity
		}
	}
	value := all[bestName]
	return bestName, value.rx, value.tx, true
}

func networkLinkBPS(sysNetRoot, iface string) uint64 {
	value, err := strconv.ParseUint(strings.TrimSpace(readFile(filepath.Join(sysNetRoot, iface, "speed"))), 10, 64)
	if err != nil || value == 0 || value > ^uint64(0)/1_000_000 {
		return 0
	}
	return value * 1_000_000
}

func (t *telemetryState) processes(now time.Time, procRoot string) processMetrics {
	t.mu.Lock()
	defer t.mu.Unlock()
	if elapsed := now.Sub(t.processCachedAt); !t.processCachedAt.IsZero() && elapsed >= 0 && elapsed < processCacheTTL {
		return t.processCache
	}
	t.processCache = scanProcesses(procRoot)
	t.processCachedAt = now
	return t.processCache
}

func scanProcesses(procRoot string) processMetrics {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return processMetrics{}
	}
	var result processMetrics
	for _, entry := range entries {
		if _, err := strconv.ParseUint(entry.Name(), 10, 64); err != nil {
			continue
		}
		state, ok := processState(readFile(filepath.Join(procRoot, entry.Name(), "stat")))
		if !ok {
			continue
		}
		result.Total++
		switch state {
		case 'R':
			result.Running++
		case 'S', 'D', 'I':
			result.Sleeping++
		case 'Z':
			result.Zombie++
		}
	}
	return result
}

func processState(stat string) (byte, bool) {
	closeParen := strings.LastIndexByte(stat, ')')
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) == 0 || len(fields[0]) != 1 {
		return 0, false
	}
	return fields[0][0], true
}

func (t *telemetryState) diskIO(now time.Time, diskstatsPath, statPath, sysBlockRoot string) diskIOMetrics {
	counters, ok := readDiskCounters(diskstatsPath, statPath, sysBlockRoot)
	if !ok {
		return diskIOMetrics{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	current := diskCounterSample{At: now, Counters: counters}
	if !t.diskIOPrimed {
		t.diskIOPrev = current
		t.diskIOPrimed = true
		return diskIOMetrics{}
	}
	seconds := now.Sub(t.diskIOPrev.At).Seconds()
	previous := t.diskIOPrev.Counters
	t.diskIOPrev = current
	if seconds <= 0 {
		return diskIOMetrics{}
	}
	readBytes, readOK := counterDelta(counters.ReadBytes, previous.ReadBytes)
	writeBytes, writeOK := counterDelta(counters.WriteBytes, previous.WriteBytes)
	operations, operationsOK := counterDelta(counters.Operations, previous.Operations)
	serviceMS, serviceOK := counterDelta(counters.ServiceMS, previous.ServiceMS)
	cpuTotal, cpuOK := counterDelta(counters.CPUTotal, previous.CPUTotal)
	cpuIOWait, waitOK := counterDelta(counters.CPUIOWait, previous.CPUIOWait)
	if !readOK || !writeOK || !operationsOK || !serviceOK || !cpuOK || !waitOK {
		return diskIOMetrics{}
	}
	result := diskIOMetrics{
		ReadBPS:  float64(readBytes) / seconds,
		WriteBPS: float64(writeBytes) / seconds,
		IOPS:     float64(operations) / seconds,
	}
	if cpuTotal > 0 {
		result.IOWaitPct = float64(cpuIOWait) * 100 / float64(cpuTotal)
		if result.IOWaitPct > 100 {
			result.IOWaitPct = 100
		}
	}
	if operations > 0 {
		result.LatencyMS = float64(serviceMS) / float64(operations)
	}
	return result
}

func counterDelta(current, previous uint64) (uint64, bool) {
	if current < previous {
		return 0, false
	}
	return current - previous, true
}

func readDiskCounters(diskstatsPath, statPath, sysBlockRoot string) (diskCounters, bool) {
	f, err := os.Open(diskstatsPath)
	if err != nil {
		return diskCounters{}, false
	}
	defer f.Close()
	var result diskCounters
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 || !includeBlockDevice(sysBlockRoot, fields[2]) {
			continue
		}
		reads, e1 := strconv.ParseUint(fields[3], 10, 64)
		sectorsRead, e2 := strconv.ParseUint(fields[5], 10, 64)
		readMS, e3 := strconv.ParseUint(fields[6], 10, 64)
		writes, e4 := strconv.ParseUint(fields[7], 10, 64)
		sectorsWritten, e5 := strconv.ParseUint(fields[9], 10, 64)
		writeMS, e6 := strconv.ParseUint(fields[10], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			continue
		}
		if sectorsRead > ^uint64(0)/512 || sectorsWritten > ^uint64(0)/512 {
			continue
		}
		result.ReadBytes += sectorsRead * 512
		result.WriteBytes += sectorsWritten * 512
		result.Operations += reads + writes
		result.ServiceMS += readMS + writeMS
		found = true
	}
	result.CPUTotal, result.CPUIOWait, _ = cpuTotalAndIOWait(statPath)
	return result, found
}

func includeBlockDevice(sysBlockRoot, name string) bool {
	for _, prefix := range []string{"loop", "ram", "fd", "sr", "zram"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	path := filepath.Join(sysBlockRoot, name)
	if _, err := os.Stat(filepath.Join(path, "partition")); err == nil {
		return false
	}
	holders, err := os.ReadDir(filepath.Join(path, "holders"))
	return err == nil && len(holders) == 0
}

func cpuTotalAndIOWait(path string) (uint64, uint64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 6 || fields[0] != "cpu" {
		return 0, 0, false
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		values = append(values, value)
	}
	var total uint64
	// guest and guest_nice are already included in user and nice. Summing only
	// the first eight fields avoids double-counting those CPU ticks.
	for index, value := range values {
		if index >= 8 {
			break
		}
		total += value
	}
	return total, values[4], true
}

func readCPUTemperature(thermalRoot, hwmonRoot string) *float64 {
	var fallback *float64
	zones, _ := os.ReadDir(thermalRoot)
	for _, zone := range zones {
		if !strings.HasPrefix(zone.Name(), "thermal_zone") {
			continue
		}
		value, ok := temperatureValue(filepath.Join(thermalRoot, zone.Name(), "temp"))
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(readFile(filepath.Join(thermalRoot, zone.Name(), "type"))))
		if temperatureLabelPreferred(typ) {
			return &value
		}
		if fallback == nil {
			copy := value
			fallback = &copy
		}
	}
	hwmons, _ := os.ReadDir(hwmonRoot)
	for _, hwmon := range hwmons {
		inputs, _ := filepath.Glob(filepath.Join(hwmonRoot, hwmon.Name(), "temp*_input"))
		for _, input := range inputs {
			value, ok := temperatureValue(input)
			if !ok {
				continue
			}
			labelPath := strings.TrimSuffix(input, "_input") + "_label"
			label := strings.ToLower(strings.TrimSpace(readFile(labelPath)))
			if label == "" || temperatureLabelPreferred(label) {
				return &value
			}
			if fallback == nil {
				copy := value
				fallback = &copy
			}
		}
	}
	return fallback
}

func (t *telemetryState) cpuTemperature(now time.Time, thermalRoot, hwmonRoot string) *float64 {
	t.mu.Lock()
	if elapsed := now.Sub(t.temperatureCachedAt); !t.temperatureCachedAt.IsZero() && elapsed >= 0 && elapsed < temperatureCacheTTL {
		value := cloneFloat64(t.temperatureCache)
		t.mu.Unlock()
		return value
	}
	t.mu.Unlock()

	value := readCPUTemperature(thermalRoot, hwmonRoot)
	t.mu.Lock()
	t.temperatureCachedAt = now
	t.temperatureCache = cloneFloat64(value)
	t.mu.Unlock()
	return cloneFloat64(value)
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func temperatureValue(path string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(readFile(path)), 64)
	if err != nil {
		return 0, false
	}
	if value >= 1_000_000 || value <= -1_000_000 {
		value /= 1_000_000
	} else if value >= 1_000 || value <= -1_000 {
		value /= 1_000
	}
	return value, value >= -50 && value <= 200
}

func temperatureLabelPreferred(label string) bool {
	for _, keyword := range []string{"cpu", "package", "soc", "core", "x86_pkg"} {
		if strings.Contains(label, keyword) {
			return true
		}
	}
	return false
}
