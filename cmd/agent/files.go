package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
)

type uploadSession struct {
	file     *os.File
	tmpPath  string
	target   string
	mode     os.FileMode
	expected int64
	written  int64
}

func (a *agent) virtualPath(p string) string {
	p = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(strings.TrimSpace(p), "/")))
	if p == "." || p == "" {
		return "/"
	}
	return p
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (a *agent) resolveFilePath(p string) (string, error) {
	virtual := a.virtualPath(p)
	rel := strings.TrimPrefix(virtual, "/")
	root := filepath.Clean(a.cfg.FileBrowserRoot)
	real := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !withinRoot(root, real) {
		return "", errors.New("path escapes file_browser_root")
	}
	return real, nil
}

func (a *agent) resolveExistingFilePath(p string) (string, error) {
	real, err := a.resolveFilePath(p)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(a.cfg.FileBrowserRoot)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		return "", err
	}
	if !withinRoot(filepath.Clean(root), filepath.Clean(resolved)) {
		return "", errors.New("resolved path escapes file_browser_root")
	}
	return resolved, nil
}

func (a *agent) resolveCreateFilePath(p string) (string, error) {
	real, err := a.resolveFilePath(p)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(a.cfg.FileBrowserRoot)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(real))
	if err != nil {
		return "", err
	}
	if !withinRoot(filepath.Clean(root), filepath.Clean(parent)) {
		return "", errors.New("resolved parent escapes file_browser_root")
	}
	return filepath.Join(parent, filepath.Base(real)), nil
}

func (a *agent) resolveEntryFilePath(p string) (string, error) {
	return a.resolveCreateFilePath(p)
}

func (a *agent) fileDisabled(c *websocket.Conn, m protocol.Message) bool {
	if a.cfg.FileBrowserEnabled {
		return false
	}
	_ = a.send(c, protocol.Message{Type: "file_result", RequestID: m.RequestID, Error: "file browser disabled by agent config"})
	return true
}

func (a *agent) handleFileList(c *websocket.Conn, m protocol.Message) {
	if a.fileDisabled(c, m) {
		return
	}
	real, err := a.resolveExistingFilePath(m.Path)
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	items, err := os.ReadDir(real)
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	entries := make([]protocol.FileEntry, 0, len(items))
	virtualBase := a.virtualPath(m.Path)
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			continue
		}
		child := filepath.ToSlash(filepath.Join(virtualBase, item.Name()))
		if virtualBase == "/" {
			child = "/" + item.Name()
		}
		entries = append(entries, protocol.FileEntry{
			Name: item.Name(), Path: child, Size: info.Size(), Mode: uint32(info.Mode().Perm()),
			ModTime: info.ModTime().Unix(), IsDir: item.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	_ = a.send(c, protocol.Message{Type: "file_list_result", RequestID: m.RequestID, Path: virtualBase, Entries: entries})
}

func (a *agent) handleFileMkdir(c *websocket.Conn, m protocol.Message) {
	if a.fileDisabled(c, m) {
		return
	}
	real, err := a.resolveCreateFilePath(m.Path)
	if err == nil {
		err = os.Mkdir(real, 0755)
	}
	a.sendFileResult(c, m.RequestID, err)
}

func (a *agent) handleFileDelete(c *websocket.Conn, m protocol.Message) {
	if a.fileDisabled(c, m) {
		return
	}
	if a.virtualPath(m.Path) == "/" {
		a.sendFileError(c, m.RequestID, errors.New("refusing to remove root"))
		return
	}
	real, err := a.resolveEntryFilePath(m.Path)
	if err == nil {
		err = os.Remove(real)
	} // intentionally non-recursive
	a.sendFileResult(c, m.RequestID, err)
}

func (a *agent) handleFileRename(c *websocket.Conn, m protocol.Message) {
	if a.fileDisabled(c, m) {
		return
	}
	from, err := a.resolveEntryFilePath(m.Path)
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	to, err := a.resolveCreateFilePath(m.Target)
	if err == nil {
		err = os.Rename(from, to)
	}
	a.sendFileResult(c, m.RequestID, err)
}

func (a *agent) handleFileDownload(c *websocket.Conn, m protocol.Message) {
	if !a.cfg.FileBrowserEnabled {
		a.sendFileEnd(c, m.RequestID, errors.New("file browser disabled by agent config"))
		return
	}
	real, err := a.resolveExistingFilePath(m.Path)
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	f, err := os.Open(real)
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	if !info.Mode().IsRegular() {
		a.sendFileEnd(c, m.RequestID, errors.New("not a regular file"))
		return
	}
	limit := a.cfg.MaxFileTransferBytes
	if m.Size > 0 && (limit == 0 || m.Size < limit) {
		limit = m.Size
	}
	if limit > 0 && info.Size() > limit {
		a.sendFileEnd(c, m.RequestID, fmt.Errorf("file exceeds configured maximum (%d bytes)", limit))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.fileMu.Lock()
	a.downloads[m.RequestID] = cancel
	a.fileMu.Unlock()
	defer func() {
		a.fileMu.Lock()
		delete(a.downloads, m.RequestID)
		a.fileMu.Unlock()
		cancel()
	}()

	if err := a.send(c, protocol.Message{Type: "file_meta", RequestID: m.RequestID, Size: info.Size(), Mode: uint32(info.Mode().Perm())}); err != nil {
		return
	}
	buf := make([]byte, a.cfg.FileTransferChunkBytes)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if err := a.send(c, protocol.Message{Type: "file_chunk", RequestID: m.RequestID, Data: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			a.sendFileEnd(c, m.RequestID, readErr)
			return
		}
	}
	_ = a.send(c, protocol.Message{Type: "file_end", RequestID: m.RequestID})
}

func (a *agent) handleFileCancel(m protocol.Message) {
	a.fileMu.Lock()
	cancel := a.downloads[m.RequestID]
	a.fileMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *agent) handleFileUploadStart(c *websocket.Conn, m protocol.Message) {
	if !a.cfg.FileBrowserEnabled {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: "file browser disabled by agent config"})
		return
	}
	if a.cfg.MaxFileTransferBytes > 0 && m.Size > a.cfg.MaxFileTransferBytes {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: "file exceeds configured maximum"})
		return
	}
	target, err := a.resolveCreateFilePath(m.Path)
	if err != nil {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".homectl-upload-*")
	if err != nil {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	mode := os.FileMode(m.Mode)
	if mode == 0 {
		mode = 0644
	}
	u := &uploadSession{file: f, tmpPath: f.Name(), target: target, mode: mode, expected: m.Size}
	a.fileMu.Lock()
	if old := a.uploads[m.RequestID]; old != nil {
		old.file.Close()
		os.Remove(old.tmpPath)
	}
	a.uploads[m.RequestID] = u
	a.fileMu.Unlock()
	_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID})
}

func (a *agent) handleFileUploadChunk(c *websocket.Conn, m protocol.Message) {
	a.fileMu.Lock()
	u := a.uploads[m.RequestID]
	a.fileMu.Unlock()
	if u == nil {
		return
	}
	b, err := base64.StdEncoding.DecodeString(m.Data)
	if err == nil && a.cfg.MaxFileTransferBytes > 0 && u.written+int64(len(b)) > a.cfg.MaxFileTransferBytes {
		err = errors.New("file exceeds configured maximum")
	}
	if err == nil {
		var n int
		n, err = u.file.Write(b)
		u.written += int64(n)
	}
	if err != nil {
		a.abortUpload(m.RequestID)
		_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: m.RequestID, Error: err.Error()})
	}
}

func (a *agent) handleFileUploadEnd(c *websocket.Conn, m protocol.Message) {
	a.fileMu.Lock()
	u := a.uploads[m.RequestID]
	delete(a.uploads, m.RequestID)
	a.fileMu.Unlock()
	if u == nil {
		_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: m.RequestID, Error: "upload session not found"})
		return
	}
	err := u.file.Sync()
	if closeErr := u.file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && u.expected >= 0 && u.expected != u.written {
		err = fmt.Errorf("upload size mismatch: expected %d, received %d", u.expected, u.written)
	}
	if err == nil {
		err = os.Chmod(u.tmpPath, u.mode)
	}
	if err == nil {
		err = os.Rename(u.tmpPath, u.target)
	}
	if err != nil {
		_ = os.Remove(u.tmpPath)
	}
	res := protocol.Message{Type: "file_upload_result", RequestID: m.RequestID, Size: u.written}
	if err != nil {
		res.Error = err.Error()
	}
	_ = a.send(c, res)
}

func (a *agent) handleFileUploadAbort(m protocol.Message) { a.abortUpload(m.RequestID) }

func (a *agent) abortUpload(id string) {
	a.fileMu.Lock()
	u := a.uploads[id]
	delete(a.uploads, id)
	a.fileMu.Unlock()
	if u != nil {
		_ = u.file.Close()
		_ = os.Remove(u.tmpPath)
	}
}

func (a *agent) closeFileTransfers() {
	a.fileMu.Lock()
	for _, cancel := range a.downloads {
		cancel()
	}
	for id, u := range a.uploads {
		_ = u.file.Close()
		_ = os.Remove(u.tmpPath)
		delete(a.uploads, id)
	}
	a.downloads = make(map[string]context.CancelFunc)
	a.fileMu.Unlock()
}

func (a *agent) sendFileResult(c *websocket.Conn, id string, err error) {
	m := protocol.Message{Type: "file_result", RequestID: id}
	if err != nil {
		m.Error = err.Error()
	}
	_ = a.send(c, m)
}
func (a *agent) sendFileError(c *websocket.Conn, id string, err error) { a.sendFileResult(c, id, err) }
func (a *agent) sendFileEnd(c *websocket.Conn, id string, err error) {
	m := protocol.Message{Type: "file_end", RequestID: id}
	if err != nil {
		m.Error = err.Error()
	}
	_ = a.send(c, m)
}
