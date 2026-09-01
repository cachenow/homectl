package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/sys/unix"
)

func testWebSocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverConn := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverConn <- c
		<-release
		c.CloseNow()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	cancel()
	if err != nil {
		close(release)
		ts.Close()
		t.Fatal(err)
	}
	server := <-serverConn
	t.Cleanup(func() {
		client.CloseNow()
		close(release)
		ts.Close()
	})
	return server, client
}

func TestRootRelativePathRejectsTraversal(t *testing.T) {
	a := &agent{}
	for _, value := range []string{"..", "../etc", "/safe/../etc", "safe/..", "safe/../../etc"} {
		if _, err := a.rootRelativePath(value); err == nil {
			t.Fatalf("traversal path %q was accepted", value)
		}
	}

	got, err := a.rootRelativePath("/etc/config")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("etc", "config"); got != want {
		t.Fatalf("virtual absolute path=%q want=%q", got, want)
	}
}

func TestOpenRegularFileForDownloadRejectsSpecialFilesAndEscapes(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "parent-escape")); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, info, err := openRegularFileForDownload(root, "regular")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("regular file reported mode %v", info.Mode())
	}
	_ = f.Close()

	for _, value := range []string{"fifo", "escape", filepath.Join("parent-escape", "secret")} {
		if f, _, err := openRegularFileForDownload(root, value); err == nil {
			_ = f.Close()
			t.Fatalf("unsafe download path %q was accepted", value)
		}
	}
}

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := renameNoReplace(root, "source", "target"); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "target")); err != nil || string(got) != "target" {
		t.Fatalf("destination changed: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "source")); err != nil || string(got) != "source" {
		t.Fatalf("source changed after rejected rename: %q err=%v", got, err)
	}

	if err := renameNoReplace(root, "source", "renamed"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "renamed")); err != nil || string(got) != "source" {
		t.Fatalf("renamed file=%q err=%v", got, err)
	}
}

func TestAbortUploadRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile(".homectl-upload-test", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{uploads: map[string]*uploadSession{
		"request": {root: root, file: f, tmpRel: ".homectl-upload-test", target: "target"},
	}}
	a.abortUpload("request")
	if _, err := os.Stat(filepath.Join(dir, ".homectl-upload-test")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary upload remains: %v", err)
	}
	if len(a.uploads) != 0 {
		t.Fatalf("upload session remains: %#v", a.uploads)
	}
}

func TestCloseFileTransfersCancelsDownloadsAndCleansUploads(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, err := root.OpenFile(".homectl-upload-test", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &agent{
		uploads: map[string]*uploadSession{
			"upload": {root: root, file: f, tmpRel: ".homectl-upload-test", target: "target"},
		},
		downloads: map[string]*downloadSession{"download": {cancel: cancel}},
	}
	a.closeFileTransfers()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("download was not canceled")
	}
	if _, err := os.Stat(filepath.Join(dir, ".homectl-upload-test")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary upload remains: %v", err)
	}
	if len(a.uploads) != 0 || len(a.downloads) != 0 {
		t.Fatalf("transfer maps not cleared: uploads=%d downloads=%d", len(a.uploads), len(a.downloads))
	}
}

func TestCreditUploadWritesInWorkerAndCommitsAtomically(t *testing.T) {
	dir := t.TempDir()
	serverConn, clientConn := testWebSocketPair(t)
	a := &agent{
		cfg: agentConfig{
			FileBrowserEnabled:   true,
			FileBrowserRoot:      dir,
			MaxFileTransferBytes: 1024,
			writeTimeoutDur:      time.Second,
		},
		uploads: make(map[string]*uploadSession),
	}
	a.handleFileUploadStart(serverConn, protocol.Message{
		Type: "file_upload_start", RequestID: "credit-upload", Path: "/target", Size: 5, Mode: 0600, Credits: 2,
	})
	read := func() protocol.Message {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var m protocol.Message
		if err := wsjson.Read(ctx, clientConn, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if ready := read(); ready.Type != "file_upload_ready" || ready.Credits != 2 || ready.Error != "" {
		t.Fatalf("upload ready=%#v", ready)
	}
	a.handleFileUploadChunk(serverConn, protocol.Message{
		Type: "file_upload_chunk", RequestID: "credit-upload", Data: base64.StdEncoding.EncodeToString([]byte("hello")),
	})
	if credit := read(); credit.Type != "file_upload_credit" || credit.Credits != 1 {
		t.Fatalf("upload credit=%#v", credit)
	}
	a.handleFileUploadEnd(serverConn, protocol.Message{Type: "file_upload_end", RequestID: "credit-upload"})
	if result := read(); result.Type != "file_upload_result" || result.Error != "" || result.Size != 5 {
		t.Fatalf("upload result=%#v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "target"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("uploaded file=%q err=%v", data, err)
	}
	info, err := os.Stat(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("uploaded mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestDownloadStreamsExactlyAdvertisedSnapshotSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	serverConn, clientConn := testWebSocketPair(t)
	a := &agent{
		cfg: agentConfig{
			FileBrowserEnabled:     true,
			FileBrowserRoot:        dir,
			FileTransferChunkBytes: 4,
			MaxFileTransferBytes:   1024,
			writeTimeoutDur:        time.Second,
		},
		downloads: make(map[string]*downloadSession),
	}
	go a.handleFileDownload(serverConn, protocol.Message{
		Type: "file_download", RequestID: "snapshot", Path: "/source", Credits: 1,
	})
	read := func() protocol.Message {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var m protocol.Message
		if err := wsjson.Read(ctx, clientConn, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	meta := read()
	if meta.Type != "file_meta" || meta.Size != 5 {
		t.Fatalf("download metadata=%#v", meta)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("-appended"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var downloaded []byte
	for {
		m := read()
		switch m.Type {
		case "file_chunk":
			b, err := base64.StdEncoding.DecodeString(m.Data)
			if err != nil {
				t.Fatal(err)
			}
			downloaded = append(downloaded, b...)
			a.handleFileCredit(protocol.Message{RequestID: "snapshot", Credits: 1})
		case "file_end":
			if m.Error != "" {
				t.Fatalf("download ended with error %q", m.Error)
			}
			if string(downloaded) != "hello" {
				t.Fatalf("downloaded %q, want original advertised bytes", downloaded)
			}
			return
		}
	}
}
