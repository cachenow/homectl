package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"homectl/internal/protocol"
)

func (s *Server) requireFileBrowser(w http.ResponseWriter) bool {
	if !s.cfg.FileBrowserEnabled {
		http.Error(w, "file browser disabled", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "file_list", Path: path}, s.cfg.FileTransferTimeout)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.Error != "" {
		http.Error(w, res.Error, http.StatusBadGateway)
		return
	}
	if res.Path != "" {
		path = res.Path
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "entries": res.Entries})
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil || strings.TrimSpace(in.Path) == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "file_mkdir", Path: in.Path}, s.cfg.FileTransferTimeout)
	writeFileOpResult(w, res, err)
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in) != nil || strings.TrimSpace(in.Path) == "" || filepath.Clean(in.Path) == "/" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "file_delete", Path: in.Path}, s.cfg.FileTransferTimeout)
	writeFileOpResult(w, res, err)
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	var in struct {
		Path   string `json:"path"`
		Target string `json:"target"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&in) != nil || strings.TrimSpace(in.Path) == "" || strings.TrimSpace(in.Target) == "" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	res, err := s.request(r.PathValue("id"), protocol.Message{Type: "file_rename", Path: in.Path, Target: in.Target}, s.cfg.FileTransferTimeout)
	writeFileOpResult(w, res, err)
}

func writeFileOpResult(w http.ResponseWriter, res protocol.Message, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.Error != "" {
		http.Error(w, res.Error, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	agent, err := s.getOnlineAgent(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	requestID, p := s.newPending()
	defer s.removePending(requestID, p)
	if err := s.sendAgent(agent, protocol.Message{Type: "file_download", RequestID: requestID, Path: path, Size: s.cfg.MaxFileTransferBytes}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	timer := time.NewTimer(s.cfg.FileTransferTimeout)
	defer timer.Stop()
	headersWritten := false
	for {
		select {
		case m := <-p.ch:
			resetTimer(timer, s.cfg.FileTransferTimeout)
			switch m.Type {
			case "file_meta":
				if m.Error != "" {
					http.Error(w, m.Error, http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
				if m.Size >= 0 {
					w.Header().Set("Content-Length", int64String(m.Size))
				}
				headersWritten = true
			case "file_chunk":
				b, err := base64.StdEncoding.DecodeString(m.Data)
				if err != nil {
					return
				}
				if !headersWritten {
					w.Header().Set("Content-Type", "application/octet-stream")
					headersWritten = true
				}
				if _, err := w.Write(b); err != nil {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			case "file_end":
				if m.Error != "" && !headersWritten {
					http.Error(w, m.Error, http.StatusBadGateway)
				}
				return
			}
		case <-timer.C:
			_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
			if !headersWritten {
				http.Error(w, "file transfer timeout", http.StatusGatewayTimeout)
			}
			return
		case <-r.Context().Done():
			_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
			return
		}
	}
}

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	if !s.requireFileBrowser(w) {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	if s.cfg.MaxFileTransferBytes > 0 && r.ContentLength > s.cfg.MaxFileTransferBytes {
		http.Error(w, "file exceeds configured maximum", http.StatusRequestEntityTooLarge)
		return
	}
	agent, err := s.getOnlineAgent(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	requestID, p := s.newPending()
	defer s.removePending(requestID, p)
	if err := s.sendAgent(agent, protocol.Message{Type: "file_upload_start", RequestID: requestID, Path: path, Size: r.ContentLength, Mode: 0644}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	wait := func(expected string) (protocol.Message, error) {
		t := time.NewTimer(s.cfg.FileTransferTimeout)
		defer t.Stop()
		for {
			select {
			case m := <-p.ch:
				if m.Type == expected {
					return m, nil
				}
			case <-t.C:
				return protocol.Message{}, errors.New("file transfer timeout")
			case <-r.Context().Done():
				return protocol.Message{}, r.Context().Err()
			}
		}
	}
	ready, err := wait("file_upload_ready")
	if err != nil || ready.Error != "" {
		if err == nil {
			err = errors.New(ready.Error)
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	buf := make([]byte, s.cfg.FileTransferChunkBytes)
	var total int64
	for {
		n, readErr := r.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			if s.cfg.MaxFileTransferBytes > 0 && total > s.cfg.MaxFileTransferBytes {
				_ = s.sendAgent(agent, protocol.Message{Type: "file_upload_abort", RequestID: requestID})
				http.Error(w, "file exceeds configured maximum", http.StatusRequestEntityTooLarge)
				return
			}
			if err := s.sendAgent(agent, protocol.Message{Type: "file_upload_chunk", RequestID: requestID, Data: base64.StdEncoding.EncodeToString(buf[:n])}); err != nil {
				_ = s.sendAgent(agent, protocol.Message{Type: "file_upload_abort", RequestID: requestID})
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = s.sendAgent(agent, protocol.Message{Type: "file_upload_abort", RequestID: requestID})
			http.Error(w, readErr.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.sendAgent(agent, protocol.Message{Type: "file_upload_end", RequestID: requestID}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	res, err := wait("file_upload_result")
	if err != nil {
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
		return
	}
	if res.Error != "" {
		http.Error(w, res.Error, http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": total})
}

func int64String(v int64) string { return strconv.FormatInt(v, 10) }
