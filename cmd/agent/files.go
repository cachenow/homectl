package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"homectl/internal/protocol"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

const (
	maxFileDownloadCredits   = 8
	maxFileUploadCredits     = 4
	maxConcurrentFileUploads = 4
)

type uploadSession struct {
	root     *os.Root
	file     *os.File
	tmpRel   string
	target   string
	mode     os.FileMode
	expected int64
	written  int64

	creditMode bool
	events     chan protocol.Message
	stop       chan struct{}
	stopOnce   sync.Once
}

func (u *uploadSession) signalStop() {
	if u == nil || !u.creditMode {
		return
	}
	u.stopOnce.Do(func() { close(u.stop) })
}

type downloadSession struct {
	cancel  context.CancelFunc
	credits chan struct{}
}

func (a *agent) virtualPath(p string) string {
	p = filepath.ToSlash(filepath.Clean("/" + strings.TrimPrefix(strings.TrimSpace(p), "/")))
	if p == "." || p == "" {
		return "/"
	}
	return p
}

// rootRelativePath converts the UI's absolute-looking virtual path (/foo/bar)
// to a local relative path for os.Root. os.Root remains the security boundary;
// this function exists only to normalize the UI representation.
func (a *agent) rootRelativePath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	for _, segment := range strings.Split(filepath.ToSlash(raw), "/") {
		if segment == ".." {
			return "", errors.New("invalid file path")
		}
	}
	virtual := a.virtualPath(p)
	rel := strings.TrimPrefix(virtual, "/")
	if rel == "" {
		return ".", nil
	}
	local, err := filepath.Localize(rel)
	if err != nil {
		return "", errors.New("invalid file path")
	}
	return local, nil
}

func (a *agent) openFileRoot() (*os.Root, error) {
	return os.OpenRoot(a.cfg.FileBrowserRoot)
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
	root, err := a.openFileRoot()
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	defer root.Close()
	rel, err := a.rootRelativePath(m.Path)
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	dir, err := root.Open(rel)
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	items, err := dir.ReadDir(-1)
	_ = dir.Close()
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
			ModTime: info.ModTime().Unix(), IsDir: item.IsDir(), IsSymlink: item.Type()&os.ModeSymlink != 0,
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
	root, err := a.openFileRoot()
	if err == nil {
		defer root.Close()
		var rel string
		rel, err = a.rootRelativePath(m.Path)
		if err == nil {
			err = root.Mkdir(rel, 0755)
		}
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
	root, err := a.openFileRoot()
	if err == nil {
		defer root.Close()
		var rel string
		rel, err = a.rootRelativePath(m.Path)
		if err == nil {
			err = root.Remove(rel) // intentionally non-recursive
		}
	}
	a.sendFileResult(c, m.RequestID, err)
}

func (a *agent) handleFileRename(c *websocket.Conn, m protocol.Message) {
	if a.fileDisabled(c, m) {
		return
	}
	root, err := a.openFileRoot()
	if err != nil {
		a.sendFileError(c, m.RequestID, err)
		return
	}
	defer root.Close()
	from, err := a.rootRelativePath(m.Path)
	if err != nil || from == "." {
		if err == nil {
			err = errors.New("refusing to rename root")
		}
		a.sendFileError(c, m.RequestID, err)
		return
	}
	to, err := a.rootRelativePath(m.Target)
	if err == nil && to == "." {
		err = errors.New("refusing to replace root")
	}
	if err == nil {
		err = renameNoReplace(root, from, to)
	}
	a.sendFileResult(c, m.RequestID, err)
}

func renameNoReplace(root *os.Root, from, to string) error {
	if from == "." || to == "." {
		return errors.New("root cannot be renamed")
	}
	fromDir, err := root.Open(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer fromDir.Close()
	toDir, err := root.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer toDir.Close()
	if err := unix.Renameat2(int(fromDir.Fd()), filepath.Base(from), int(toDir.Fd()), filepath.Base(to), unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return fmt.Errorf("filesystem does not support atomic no-replace rename: %w", err)
		}
		return err
	}
	return nil
}

func openRegularFileForDownload(root *os.Root, rel string) (*os.File, os.FileInfo, error) {
	// Check the directory entry before opening so a FIFO or device cannot block
	// or cause side effects. os.Root keeps both checks beneath the configured
	// browser root even if an entry is replaced concurrently.
	info, err := root.Stat(rel)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("not a regular file")
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, nil, err
	}
	return f, info, nil
}

func (a *agent) handleFileDownload(c *websocket.Conn, m protocol.Message) {
	if !a.cfg.FileBrowserEnabled {
		a.sendFileEnd(c, m.RequestID, errors.New("file browser disabled by agent config"))
		return
	}
	root, err := a.openFileRoot()
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	defer root.Close()
	rel, err := a.rootRelativePath(m.Path)
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	f, info, err := openRegularFileForDownload(root, rel)
	if err != nil {
		a.sendFileEnd(c, m.RequestID, err)
		return
	}
	defer f.Close()
	limit := a.cfg.MaxFileTransferBytes
	if m.Size > 0 && (limit == 0 || m.Size < limit) {
		limit = m.Size
	}
	if limit > 0 && info.Size() > limit {
		a.sendFileEnd(c, m.RequestID, fmt.Errorf("file exceeds configured maximum (%d bytes)", limit))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	download := &downloadSession{cancel: cancel}
	if m.Credits > 0 {
		download.credits = make(chan struct{}, maxFileDownloadCredits)
		for i := 0; i < min(m.Credits, maxFileDownloadCredits); i++ {
			download.credits <- struct{}{}
		}
	}
	a.fileMu.Lock()
	old := a.downloads[m.RequestID]
	a.downloads[m.RequestID] = download
	a.fileMu.Unlock()
	if old != nil {
		old.cancel()
	}
	defer func() {
		a.fileMu.Lock()
		if a.downloads[m.RequestID] == download {
			delete(a.downloads, m.RequestID)
		}
		a.fileMu.Unlock()
		cancel()
	}()

	if err := a.send(c, protocol.Message{Type: "file_meta", RequestID: m.RequestID, Size: info.Size(), Mode: uint32(info.Mode().Perm())}); err != nil {
		return
	}
	buf := make([]byte, a.cfg.FileTransferChunkBytes)
	remaining := info.Size()
	for remaining > 0 {
		if download.credits != nil {
			select {
			case <-ctx.Done():
				return
			case <-download.credits:
			}
		} else {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
		readBuf := buf
		if int64(len(readBuf)) > remaining {
			readBuf = readBuf[:remaining]
		}
		n, readErr := f.Read(readBuf)
		if n > 0 {
			remaining -= int64(n)
			if err := a.send(c, protocol.Message{Type: "file_chunk", RequestID: m.RequestID, Data: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			if remaining > 0 {
				a.sendFileEnd(c, m.RequestID, io.ErrUnexpectedEOF)
				return
			}
			break
		}
		if readErr != nil {
			a.sendFileEnd(c, m.RequestID, readErr)
			return
		}
		if n == 0 {
			a.sendFileEnd(c, m.RequestID, io.ErrNoProgress)
			return
		}
	}
	_ = a.send(c, protocol.Message{Type: "file_end", RequestID: m.RequestID})
}

func (a *agent) startFileDownload(c *websocket.Conn, m protocol.Message) {
	if !acquireSlot(a.fileDownloadSlots) {
		a.sendFileEnd(c, m.RequestID, errors.New("too many concurrent file downloads"))
		return
	}
	release := releaseSlot(a.fileDownloadSlots)
	go func() {
		defer release()
		a.handleFileDownload(c, m)
	}()
}

func (a *agent) startFileOp(c *websocket.Conn, m protocol.Message, operation func(*websocket.Conn, protocol.Message)) {
	if !acquireSlot(a.fileOpSlots) {
		a.sendFileError(c, m.RequestID, errors.New("too many concurrent file operations"))
		return
	}
	release := releaseSlot(a.fileOpSlots)
	go func() {
		defer release()
		operation(c, m)
	}()
}

func (a *agent) handleFileCancel(m protocol.Message) {
	a.fileMu.Lock()
	download := a.downloads[m.RequestID]
	a.fileMu.Unlock()
	if download != nil {
		download.cancel()
	}
}

func (a *agent) handleFileCredit(m protocol.Message) {
	if m.Credits <= 0 {
		return
	}
	a.fileMu.Lock()
	download := a.downloads[m.RequestID]
	a.fileMu.Unlock()
	if download == nil || download.credits == nil {
		return
	}
	for i := 0; i < min(m.Credits, cap(download.credits)); i++ {
		select {
		case download.credits <- struct{}{}:
		default:
			return
		}
	}
}

func randomUploadSuffix() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *agent) handleFileUploadStart(c *websocket.Conn, m protocol.Message) {
	if !a.cfg.FileBrowserEnabled {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: "file browser disabled by agent config"})
		return
	}
	if m.Size < -1 || (a.cfg.MaxFileTransferBytes > 0 && m.Size > a.cfg.MaxFileTransferBytes) {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: "file exceeds configured maximum"})
		return
	}
	root, err := a.openFileRoot()
	if err != nil {
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	target, err := a.rootRelativePath(m.Path)
	if err != nil || target == "." {
		root.Close()
		if err == nil {
			err = errors.New("invalid upload target")
		}
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	parent := filepath.Dir(target)
	suffix, err := randomUploadSuffix()
	if err != nil {
		root.Close()
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	tmpRel := filepath.Join(parent, ".homectl-upload-"+suffix)
	f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		root.Close()
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: err.Error()})
		return
	}
	mode := os.FileMode(m.Mode).Perm()
	if mode == 0 {
		mode = 0644
	}
	u := &uploadSession{root: root, file: f, tmpRel: tmpRel, target: target, mode: mode, expected: m.Size}
	if m.Credits > 0 {
		u.creditMode = true
		u.events = make(chan protocol.Message, min(m.Credits, maxFileUploadCredits))
		u.stop = make(chan struct{})
	}
	a.fileMu.Lock()
	old := a.uploads[m.RequestID]
	if old == nil && len(a.uploads) >= maxConcurrentFileUploads {
		a.fileMu.Unlock()
		_ = f.Close()
		_ = root.Remove(tmpRel)
		_ = root.Close()
		_ = a.send(c, protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID, Error: "too many concurrent file uploads"})
		return
	}
	a.uploads[m.RequestID] = u
	a.fileMu.Unlock()
	if old != nil {
		a.stopUpload(old)
	}
	ready := protocol.Message{Type: "file_upload_ready", RequestID: m.RequestID}
	if u.creditMode {
		go a.runCreditUpload(c, m.RequestID, u)
		ready.Credits = cap(u.events)
	}
	_ = a.send(c, ready)
}

func (a *agent) handleFileUploadChunk(c *websocket.Conn, m protocol.Message) {
	a.fileMu.Lock()
	u := a.uploads[m.RequestID]
	a.fileMu.Unlock()
	if u == nil {
		return
	}
	if u.creditMode {
		a.queueCreditUploadEvent(c, m.RequestID, u, m)
		return
	}
	err := a.writeUploadChunk(u, m.Data)
	if err != nil {
		a.abortUpload(m.RequestID)
		_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: m.RequestID, Error: err.Error()})
	}
}

func (a *agent) handleFileUploadEnd(c *websocket.Conn, m protocol.Message) {
	a.fileMu.Lock()
	u := a.uploads[m.RequestID]
	if u != nil && !u.creditMode {
		delete(a.uploads, m.RequestID)
	}
	a.fileMu.Unlock()
	if u == nil {
		_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: m.RequestID, Error: "upload session not found"})
		return
	}
	if u.creditMode {
		a.queueCreditUploadEvent(c, m.RequestID, u, m)
		return
	}
	res := a.finishUpload(m.RequestID, u)
	a.cleanupUpload(u)
	_ = a.send(c, res)
}

func (a *agent) writeUploadChunk(u *uploadSession, encoded string) error {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	if len(b) > 512<<10 {
		return errors.New("file upload chunk is too large")
	}
	chunkSize := int64(len(b))
	if u.written > 1<<63-1-chunkSize {
		return errors.New("file size overflow")
	}
	if a.cfg.MaxFileTransferBytes > 0 && (u.written > a.cfg.MaxFileTransferBytes || chunkSize > a.cfg.MaxFileTransferBytes-u.written) {
		return errors.New("file exceeds configured maximum")
	}
	n, err := u.file.Write(b)
	u.written += int64(n)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

func (a *agent) finishUpload(id string, u *uploadSession) protocol.Message {
	err := u.file.Sync()
	if err == nil && u.expected >= 0 && u.expected != u.written {
		err = fmt.Errorf("upload size mismatch: expected %d, received %d", u.expected, u.written)
	}
	// Chmod the already-open descriptor, avoiding a path-based chmod race.
	if err == nil {
		err = u.file.Chmod(u.mode)
	}
	if closeErr := u.file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = renameNoReplace(u.root, u.tmpRel, u.target)
	}
	res := protocol.Message{Type: "file_upload_result", RequestID: id, Size: u.written}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func (a *agent) queueCreditUploadEvent(c *websocket.Conn, id string, u *uploadSession, m protocol.Message) {
	select {
	case <-u.stop:
		return
	case u.events <- m:
		return
	default:
	}
	if a.removeUpload(id, u) {
		u.signalStop()
		_ = u.file.Close()
		go func() {
			_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: id, Error: "file upload exceeded its bounded queue"})
		}()
	}
}

func (a *agent) runCreditUpload(c *websocket.Conn, id string, u *uploadSession) {
	defer a.cleanupUpload(u)
	for {
		select {
		case <-u.stop:
			return
		case m := <-u.events:
			select {
			case <-u.stop:
				return
			default:
			}
			switch m.Type {
			case "file_upload_chunk":
				if err := a.writeUploadChunk(u, m.Data); err != nil {
					if a.removeUpload(id, u) {
						_ = a.send(c, protocol.Message{Type: "file_upload_result", RequestID: id, Error: err.Error()})
					}
					return
				}
				if err := a.send(c, protocol.Message{Type: "file_upload_credit", RequestID: id, Credits: 1}); err != nil {
					a.removeUpload(id, u)
					return
				}
			case "file_upload_end":
				if !a.removeUpload(id, u) {
					return
				}
				_ = a.send(c, a.finishUpload(id, u))
				return
			}
		}
	}
}

func (a *agent) handleFileUploadAbort(m protocol.Message) { a.abortUpload(m.RequestID) }

func (a *agent) abortUpload(id string) {
	a.fileMu.Lock()
	u := a.uploads[id]
	delete(a.uploads, id)
	a.fileMu.Unlock()
	if u != nil {
		a.stopUpload(u)
	}
}

func (a *agent) removeUpload(id string, u *uploadSession) bool {
	a.fileMu.Lock()
	owned := a.uploads[id] == u
	if owned {
		delete(a.uploads, id)
	}
	a.fileMu.Unlock()
	return owned
}

func (a *agent) stopUpload(u *uploadSession) {
	if u.creditMode {
		u.signalStop()
		_ = u.file.Close()
		return
	}
	a.cleanupUpload(u)
}

func (a *agent) cleanupUpload(u *uploadSession) {
	_ = u.file.Close()
	_ = u.root.Remove(u.tmpRel)
	_ = u.root.Close()
}

func (a *agent) closeFileTransfers() {
	a.fileMu.Lock()
	downloads := a.downloads
	uploads := a.uploads
	a.downloads = make(map[string]*downloadSession)
	a.uploads = make(map[string]*uploadSession)
	a.fileMu.Unlock()
	for _, download := range downloads {
		download.cancel()
	}
	for _, u := range uploads {
		a.stopUpload(u)
	}
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
