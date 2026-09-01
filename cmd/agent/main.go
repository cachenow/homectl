package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	randv2 "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/creack/pty"
)

var version = "dev"

const (
	maxConcurrentActions       = 1
	maxConcurrentExec          = 4
	maxConcurrentTerminals     = 4
	maxConcurrentFileOps       = 8
	maxConcurrentFileDownloads = 4
	termInputQueueDepth        = 16
	maxTermInputBytes          = 64 << 10
	systemActionTimeout        = 15 * time.Second
)

type termSession struct {
	f       *os.File
	cmd     *exec.Cmd
	input   chan []byte
	done    chan struct{}
	release func()
	once    sync.Once
}

func (t *termSession) stop() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.done != nil {
			close(t.done)
		}
		if t.f != nil {
			_ = t.f.Close()
		}
		if t.cmd != nil && t.cmd.Process != nil {
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		}
		if t.release != nil {
			t.release()
		}
	})
}

func (t *termSession) enqueueInput(data []byte) bool {
	if t == nil || t.done == nil || t.input == nil {
		return false
	}
	select {
	case <-t.done:
		return false
	default:
	}
	select {
	case <-t.done:
		return false
	case t.input <- data:
		return true
	default:
		return false
	}
}

func (t *termSession) writeInput() {
	for {
		select {
		case <-t.done:
			return
		case data := <-t.input:
			if _, err := t.f.Write(data); err != nil {
				t.stop()
				return
			}
		}
	}
}

type agent struct {
	cfg       agentConfig
	statePath string
	state     agentState

	stateMu sync.Mutex
	termMu  sync.Mutex
	terms   map[string]*termSession
	writeMu sync.Mutex

	fileMu    sync.Mutex
	uploads   map[string]*uploadSession
	downloads map[string]*downloadSession

	actionSlots       chan struct{}
	execSlots         chan struct{}
	termSlots         chan struct{}
	fileOpSlots       chan struct{}
	fileDownloadSlots chan struct{}

	cpuMu        sync.Mutex
	prevCPUTotal uint64
	prevCPUIdle  uint64
	cpuPrimed    bool
}

func main() {
	configPath := flag.String("config", defaultConfigPath(), "path to config.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("homectl-agent %s\n", version)
		return
	}

	cfg, err := loadAgentConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Name == "" {
		cfg.Name, _ = os.Hostname()
		cfg.Name = strings.TrimSpace(cfg.Name)
		if cfg.Name == "" {
			cfg.Name = "linux-agent"
		}
		if err := validateAgentName(cfg.Name); err != nil {
			log.Fatal(err)
		}
	}
	state, err := loadOrCreateState(cfg.StateFile)
	if err != nil {
		log.Fatal(err)
	}
	if state.DeviceToken == "" {
		if !protocol.ValidEnrollmentToken(cfg.EnrollToken) {
			log.Fatalf("enroll_token must be exactly %d lowercase hexadecimal characters until this device has enrolled successfully", protocol.EnrollmentTokenLength)
		}
		state.DeviceToken, err = randomHex(32)
		if err != nil {
			log.Fatal(err)
		}
		state.PendingEnrollment = true
		if err := saveState(cfg.StateFile, state); err != nil {
			log.Fatalf("save pending device identity: %v", err)
		}
	}
	if state.PendingEnrollment && !protocol.ValidEnrollmentToken(cfg.EnrollToken) {
		log.Fatalf("enroll_token must be exactly %d lowercase hexadecimal characters until this device has enrolled successfully", protocol.EnrollmentTokenLength)
	}
	if !state.PendingEnrollment {
		cfg.EnrollToken = ""
	}

	a := &agent{
		cfg:               cfg,
		statePath:         cfg.StateFile,
		state:             state,
		terms:             make(map[string]*termSession),
		uploads:           make(map[string]*uploadSession),
		downloads:         make(map[string]*downloadSession),
		actionSlots:       make(chan struct{}, maxConcurrentActions),
		execSlots:         make(chan struct{}, maxConcurrentExec),
		termSlots:         make(chan struct{}, maxConcurrentTerminals),
		fileOpSlots:       make(chan struct{}, maxConcurrentFileOps),
		fileDownloadSlots: make(chan struct{}, maxConcurrentFileDownloads),
	}
	log.Printf("homectl-agent %s starting as %s (%s), config=%s", version, cfg.Name, state.DeviceID, *configPath)

	backoff := cfg.reconnectMinDur
	for {
		connectedFor, err := a.run()
		if err != nil {
			log.Printf("connection ended: %v", err)
		}
		a.closeTerms()
		a.closeFileTransfers()
		if connectedFor >= time.Minute {
			backoff = cfg.reconnectMinDur
		}
		time.Sleep(jitteredReconnectDelay(backoff, cfg.reconnectMaxDur))
		if backoff > cfg.reconnectMaxDur-backoff {
			backoff = cfg.reconnectMaxDur
		} else {
			backoff += backoff
		}
	}
}

func jitteredReconnectDelay(base, maximum time.Duration) time.Duration {
	if base <= 0 || maximum <= 0 {
		return base
	}
	spread := base / 5
	if spread <= 0 {
		return min(base, maximum)
	}
	offset := time.Duration(randv2.Int64N(int64(spread) + 1))
	if randv2.Int64N(2) == 0 {
		return base - offset
	}
	if base > maximum-offset {
		return maximum
	}
	return base + offset
}

func (a *agent) run() (time.Duration, error) {
	a.stateMu.Lock()
	token := a.state.DeviceToken
	deviceID := a.state.DeviceID
	pendingEnrollment := a.state.PendingEnrollment
	a.stateMu.Unlock()
	enrollmentToken := ""
	if pendingEnrollment {
		enrollmentToken = a.cfg.EnrollToken
	}

	headers := http.Header{}
	if a.cfg.CloudflareAccess.ClientID != "" && a.cfg.CloudflareAccess.ClientSecret != "" {
		headers.Set("CF-Access-Client-Id", a.cfg.CloudflareAccess.ClientID)
		headers.Set("CF-Access-Client-Secret", a.cfg.CloudflareAccess.ClientSecret)
	}

	opts := &websocket.DialOptions{HTTPHeader: headers}
	if a.cfg.TLS.InsecureSkipVerify {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec -- explicit opt-in for private/self-signed deployments.
		opts.HTTPClient = &http.Client{Transport: transport}
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.dialTimeoutDur)
	c, _, err := websocket.Dial(ctx, a.cfg.Server, opts)
	cancel()
	if err != nil {
		return 0, err
	}
	defer c.CloseNow()
	c.SetReadLimit(2 << 20)

	if err := a.send(c, protocol.Message{
		Type:            "hello",
		DeviceID:        deviceID,
		Name:            a.cfg.Name,
		Token:           token,
		EnrollmentToken: enrollmentToken,
		Capabilities: []string{
			protocol.CapabilityFileDownloadCredits,
			protocol.CapabilityFileUploadCredits,
		},
	}); err != nil {
		return 0, err
	}
	var ack protocol.Message
	handshakeCtx, handshakeCancel := context.WithTimeout(context.Background(), a.cfg.handshakeTimeoutDur)
	err = wsjson.Read(handshakeCtx, c, &ack)
	handshakeCancel()
	if err != nil {
		return 0, err
	}
	if ack.Type != "hello_ack" {
		return 0, fmt.Errorf("unexpected handshake response")
	}
	if ack.DeviceToken != "" && !protocol.ValidDeviceToken(ack.DeviceToken) {
		return 0, fmt.Errorf("server returned an invalid device token")
	}
	if pendingEnrollment || ack.DeviceToken != "" {
		a.stateMu.Lock()
		if ack.DeviceToken != "" {
			a.state.DeviceToken = ack.DeviceToken
		}
		a.state.PendingEnrollment = false
		err := saveState(a.statePath, a.state)
		a.stateMu.Unlock()
		if err != nil {
			return 0, fmt.Errorf("save enrolled device identity: %w", err)
		}
		a.cfg.EnrollToken = ""
		log.Printf("enrollment complete; device identity saved to %s", a.statePath)
	}

	connectedAt := time.Now()
	log.Printf("connected to %s", a.cfg.Server)
	go a.heartbeatLoop(c)
	for {
		var m protocol.Message
		if err := wsjson.Read(context.Background(), c, &m); err != nil {
			return time.Since(connectedAt), err
		}
		switch m.Type {
		case "action":
			a.startAction(c, m)
		case "exec":
			a.startExec(c, m)
		case "term_open":
			a.startTerm(c, m)
		case "term_input":
			a.termInput(c, m)
		case "term_resize":
			a.termResize(m)
		case "term_close":
			a.termClose(m.SessionID)
		case "file_list":
			a.startFileOp(c, m, a.handleFileList)
		case "file_mkdir":
			a.startFileOp(c, m, a.handleFileMkdir)
		case "file_delete":
			a.startFileOp(c, m, a.handleFileDelete)
		case "file_rename":
			a.startFileOp(c, m, a.handleFileRename)
		case "file_download":
			a.startFileDownload(c, m)
		case "file_cancel":
			a.handleFileCancel(m)
		case "file_credit":
			a.handleFileCredit(m)
		case "file_upload_start":
			a.handleFileUploadStart(c, m)
		case "file_upload_chunk":
			a.handleFileUploadChunk(c, m)
		case "file_upload_end":
			a.handleFileUploadEnd(c, m)
		case "file_upload_abort":
			a.handleFileUploadAbort(m)
		}
	}
}

func acquireSlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseSlot(slots chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { <-slots })
	}
}

func (a *agent) startAction(c *websocket.Conn, m protocol.Message) {
	if !acquireSlot(a.actionSlots) {
		_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Error: "another system action is already in progress"})
		return
	}
	release := releaseSlot(a.actionSlots)
	go func() {
		defer release()
		a.handleAction(c, m)
	}()
}

func (a *agent) startExec(c *websocket.Conn, m protocol.Message) {
	if !acquireSlot(a.execSlots) {
		_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Error: "too many concurrent commands"})
		return
	}
	release := releaseSlot(a.execSlots)
	go func() {
		defer release()
		a.handleExec(c, m)
	}()
}

func (a *agent) startTerm(c *websocket.Conn, m protocol.Message) {
	if !acquireSlot(a.termSlots) {
		_ = a.send(c, protocol.Message{Type: "term_exit", SessionID: m.SessionID, Error: "too many concurrent terminals"})
		return
	}
	go a.openTerm(c, m, releaseSlot(a.termSlots))
}

func (a *agent) send(c *websocket.Conn, m protocol.Message) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.writeTimeoutDur)
	defer cancel()
	return wsjson.Write(ctx, c, m)
}

func (a *agent) heartbeatLoop(c *websocket.Conn) {
	t := time.NewTicker(a.cfg.heartbeatDuration)
	defer t.Stop()
	for {
		info := a.collectInfo()
		a.stateMu.Lock()
		deviceID := a.state.DeviceID
		a.stateMu.Unlock()
		if err := a.send(c, protocol.Message{Type: "heartbeat", DeviceID: deviceID, Name: a.cfg.Name, Info: &info}); err != nil {
			c.CloseNow()
			return
		}
		<-t.C
	}
}

func (a *agent) handleAction(c *websocket.Conn, m protocol.Message) {
	if m.Action != "reboot" && m.Action != "poweroff" {
		_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Error: "unsupported action"})
		return
	}
	commands := actionCommandCandidates(m.Action)
	if len(commands) == 0 {
		_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Error: "no supported system action command found"})
		return
	}
	_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Data: "accepted"})
	time.Sleep(500 * time.Millisecond)
	for _, command := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), systemActionTimeout)
		err := exec.CommandContext(ctx, command[0], command[1:]...).Run()
		cancel()
		if err == nil {
			return
		}
		log.Printf("%s failed: %v", strings.Join(command, " "), err)
	}
}

func actionCommandCandidates(action string) [][]string {
	command := "reboot"
	if action == "poweroff" {
		command = "poweroff"
	}
	var candidates [][]string
	if systemctl, err := exec.LookPath("systemctl"); err == nil {
		candidates = append(candidates, []string{systemctl, command})
	}
	if direct, err := exec.LookPath(command); err == nil {
		candidates = append(candidates, []string{direct})
	}
	return candidates
}

func (a *agent) handleExec(c *websocket.Conn, m protocol.Message) {
	if !a.cfg.ExecEnabled {
		_ = a.send(c, protocol.Message{Type: "command_result", RequestID: m.RequestID, Error: "exec disabled by agent config"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.commandTimeoutDur)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.cfg.Shell, "-lc", m.Command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	out := newCappedOutput(a.cfg.MaxCommandOutputBytes)
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	res := protocol.Message{Type: "command_result", RequestID: m.RequestID, Data: out.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.Error = "command timeout"
		res.ExitCode = -1
	} else if err != nil {
		res.Error = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
		}
	}
	_ = a.send(c, res)
}

func (a *agent) openTerm(c *websocket.Conn, m protocol.Message, release func()) {
	slotOwned := true
	defer func() {
		if slotOwned {
			release()
		}
	}()
	if !a.cfg.TerminalEnabled {
		_ = a.send(c, protocol.Message{Type: "term_exit", SessionID: m.SessionID, Error: "terminal disabled by agent config"})
		return
	}
	cols, rows := m.Cols, m.Rows
	if cols == 0 {
		cols = 100
	}
	if rows == 0 {
		rows = 30
	}
	cmd := exec.Command(a.cfg.Shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		_ = a.send(c, protocol.Message{Type: "term_exit", SessionID: m.SessionID, Error: err.Error()})
		return
	}
	t := &termSession{
		f:       f,
		cmd:     cmd,
		input:   make(chan []byte, termInputQueueDepth),
		done:    make(chan struct{}),
		release: release,
	}
	slotOwned = false
	a.termMu.Lock()
	old := a.terms[m.SessionID]
	a.terms[m.SessionID] = t
	a.termMu.Unlock()
	if old != nil {
		old.stop()
	}
	go t.writeInput()

	buf := make([]byte, 32<<10)
	var readErr error
	for {
		n, err := f.Read(buf)
		if n > 0 {
			_ = a.send(c, protocol.Message{Type: "term_data", SessionID: m.SessionID, Data: base64.StdEncoding.EncodeToString(buf[:n])})
		}
		if err != nil {
			readErr = err
			break
		}
	}
	owned := a.finishTerm(m.SessionID, t)
	_ = cmd.Wait()
	if !owned {
		return
	}
	msg := protocol.Message{Type: "term_exit", SessionID: m.SessionID}
	if readErr != io.EOF && !errors.Is(readErr, syscall.EIO) && !errors.Is(readErr, os.ErrClosed) {
		msg.Error = readErr.Error()
	}
	_ = a.send(c, msg)
}

func (a *agent) termInput(c *websocket.Conn, m protocol.Message) {
	b, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil || len(b) == 0 {
		return
	}
	a.termMu.Lock()
	t := a.terms[m.SessionID]
	a.termMu.Unlock()
	if t == nil {
		return
	}
	if len(b) <= maxTermInputBytes && t.enqueueInput(b) {
		return
	}
	a.termMu.Lock()
	owned := a.terms[m.SessionID] == t
	if owned {
		delete(a.terms, m.SessionID)
	}
	a.termMu.Unlock()
	if owned {
		t.stop()
		go func() {
			_ = a.send(c, protocol.Message{Type: "term_exit", SessionID: m.SessionID, Error: "terminal input exceeded its bounded queue"})
		}()
	}
}

func (a *agent) termResize(m protocol.Message) {
	a.termMu.Lock()
	t := a.terms[m.SessionID]
	a.termMu.Unlock()
	if t != nil && m.Cols > 0 && m.Rows > 0 {
		_ = pty.Setsize(t.f, &pty.Winsize{Cols: m.Cols, Rows: m.Rows})
	}
}

func (a *agent) termClose(id string) {
	a.termMu.Lock()
	t := a.terms[id]
	delete(a.terms, id)
	a.termMu.Unlock()
	t.stop()
}

func (a *agent) finishTerm(id string, t *termSession) bool {
	a.termMu.Lock()
	owned := a.terms[id] == t
	if owned {
		delete(a.terms, id)
	}
	a.termMu.Unlock()
	t.stop()
	return owned
}

func (a *agent) closeTerms() {
	a.termMu.Lock()
	terms := make([]*termSession, 0, len(a.terms))
	for id, t := range a.terms {
		terms = append(terms, t)
		delete(a.terms, id)
	}
	a.termMu.Unlock()
	for _, t := range terms {
		t.stop()
	}
}

func (a *agent) collectInfo() protocol.SystemInfo {
	host, _ := os.Hostname()
	diskTotal, diskFree := a.diskUsage()
	diskPhysicalTotal, diskPhysicalCount := physicalDiskCapacity()
	memTotal, memAvail := memInfo()
	return protocol.SystemInfo{
		Hostname: host, OS: osRelease(), Kernel: unameR(), Arch: runtime.GOARCH,
		CPUModel: cpuModel(), CPUCores: runtime.NumCPU(), CPUUsage: a.cpuUsage(), Load1: firstField(readFile("/proc/loadavg")),
		MemTotal: memTotal, MemAvail: memAvail,
		DiskTotal: diskTotal, DiskFree: diskFree,
		DiskPhysicalTotal: diskPhysicalTotal, DiskPhysicalCount: diskPhysicalCount,
		UptimeSec: uptime(), IPAddrs: ipAddrs(), AgentVer: version, ReportedAt: time.Now().Unix(),
	}
}

func (a *agent) diskUsage() (uint64, uint64) {
	return mountedFilesystemUsage("/proc/self/mountinfo", "/sys/dev/block", a.cfg.DiskExcludeDevicePrefixes)
}

// mountedFilesystemUsage sums mounted local block-backed filesystems. It uses
// mountinfo's major:minor device ID and /sys/dev/block instead of trusting the
// mount source path, so /dev/root, LVM, RAID and other aliases still work.
// Multiple mounts of the same filesystem are counted once.
func mountedFilesystemUsage(mountInfoPath, sysDevBlockRoot string, excludedPrefixes []string) (uint64, uint64) {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return rootDiskUsage()
	}
	defer f.Close()

	seenDevices := make(map[string]struct{})
	var total, available uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}

		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || separator+2 >= len(fields) {
			continue
		}

		deviceID := fields[2]
		mountPoint := unescapeMountField(fields[4])
		source := unescapeMountField(fields[separator+2])

		blockPath, err := filepath.EvalSymlinks(filepath.Join(sysDevBlockRoot, deviceID))
		if err != nil {
			// No sysfs block-device entry means this is normally tmpfs, proc,
			// NFS/CIFS or another non-block-backed filesystem.
			continue
		}
		blockName := filepath.Base(blockPath)
		if excludedDiskDevice(source, blockName, excludedPrefixes) {
			continue
		}
		if _, ok := seenDevices[deviceID]; ok {
			continue
		}

		var st syscall.Statfs_t
		if err := syscall.Statfs(mountPoint, &st); err != nil || st.Bsize <= 0 {
			continue
		}
		blockSize := uint64(st.Bsize)
		total += st.Blocks * blockSize
		available += st.Bavail * blockSize
		seenDevices[deviceID] = struct{}{}
	}

	if total == 0 {
		return rootDiskUsage()
	}
	return total, available
}

// physicalDiskCapacity returns the capacity of whole underlying block devices,
// independent of partitions and mount layout. Linux exposes block-device size
// in 512-byte sectors via sysfs. Devices are grouped by their canonical
// /device path and the largest block node in each group is counted, which avoids
// double-counting partitions and special sibling nodes such as eMMC boot areas.
func physicalDiskCapacity() (uint64, int) {
	return physicalDiskCapacityAt("/sys/class/block")
}

func physicalDiskCapacityAt(sysClassBlockRoot string) (uint64, int) {
	entries, err := os.ReadDir(sysClassBlockRoot)
	if err != nil {
		return 0, 0
	}

	capacityByDevice := make(map[string]uint64)
	for _, entry := range entries {
		name := entry.Name()
		blockPath := filepath.Join(sysClassBlockRoot, name)

		// A partition has its own /partition marker and must never be added to
		// the whole-device total.
		if _, err := os.Stat(filepath.Join(blockPath, "partition")); err == nil {
			continue
		}

		// loop, ram, zram, device-mapper and md devices do not normally have a
		// physical /device target. Requiring it naturally keeps logical layers
		// out of the physical-capacity total while retaining SATA/SCSI, NVMe,
		// virtio, MMC/eMMC, USB storage and persistent-memory devices.
		devicePath, err := filepath.EvalSymlinks(filepath.Join(blockPath, "device"))
		if err != nil {
			continue
		}

		sectorsText := strings.TrimSpace(readFile(filepath.Join(blockPath, "size")))
		sectors, err := strconv.ParseUint(sectorsText, 10, 64)
		if err != nil || sectors == 0 || sectors > ^uint64(0)/512 {
			continue
		}
		bytes := sectors * 512
		if bytes > capacityByDevice[devicePath] {
			capacityByDevice[devicePath] = bytes
		}
	}

	var total uint64
	for _, bytes := range capacityByDevice {
		total += bytes
	}
	return total, len(capacityByDevice)
}

func rootDiskUsage() (uint64, uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil || st.Bsize <= 0 {
		return 0, 0
	}
	blockSize := uint64(st.Bsize)
	return st.Blocks * blockSize, st.Bavail * blockSize
}

func excludedDiskDevice(source, blockName string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(source, prefix) {
			return true
		}
		namePrefix := strings.TrimPrefix(prefix, "/dev/")
		if namePrefix != "" && strings.HasPrefix(blockName, namePrefix) {
			return true
		}
	}
	return false
}

func unescapeMountField(s string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(s)
}

func (a *agent) cpuUsage() float64 {
	total, idle, ok := cpuTimes()
	if !ok {
		return -1
	}
	a.cpuMu.Lock()
	defer a.cpuMu.Unlock()
	if !a.cpuPrimed {
		a.prevCPUTotal, a.prevCPUIdle, a.cpuPrimed = total, idle, true
		return -1
	}
	dTotal := total - a.prevCPUTotal
	dIdle := idle - a.prevCPUIdle
	a.prevCPUTotal, a.prevCPUIdle = total, idle
	if dTotal == 0 || dIdle > dTotal {
		return -1
	}
	usage := float64(dTotal-dIdle) * 100 / float64(dTotal)
	if usage < 0 {
		return 0
	}
	if usage > 100 {
		return 100
	}
	return usage
}

func cpuTimes() (total, idle uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	values := make([]uint64, 0, 8)
	for _, raw := range fields[1:] {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		values = append(values, v)
		if len(values) == 8 {
			break
		}
	}
	if len(values) < 4 {
		return 0, 0, false
	}
	for _, v := range values {
		total += v
	}
	idle = values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return total, idle, true
}

func memInfo() (total, avail uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(p[1], 10, 64)
		v *= 1024
		switch strings.TrimSuffix(p[0], ":") {
		case "MemTotal":
			total = v
		case "MemAvailable":
			avail = v
		}
	}
	return
}

func uptime() uint64 {
	f := firstField(readFile("/proc/uptime"))
	v, _ := strconv.ParseFloat(f, 64)
	return uint64(v)
}

func unameR() string {
	b, _ := exec.Command("uname", "-r").Output()
	return strings.TrimSpace(string(b))
}

func osRelease() string {
	b := readFile("/etc/os-release")
	for _, l := range strings.Split(b, "\n") {
		if strings.HasPrefix(l, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), "\"")
		}
	}
	return runtime.GOOS
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "model name") || strings.HasPrefix(l, "Hardware") {
			if p := strings.SplitN(l, ":", 2); len(p) == 2 {
				return strings.TrimSpace(p[1])
			}
		}
	}
	return ""
}

func ipAddrs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, addr := range as {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && !ip.IsLoopback() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func firstField(s string) string {
	p := strings.Fields(s)
	if len(p) > 0 {
		return p[0]
	}
	return ""
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func defaultConfigPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exe), "config.json")
}
