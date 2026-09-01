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
	credits := 0
	if agent.fileDownloadCredits {
		credits = initialFileDownloadCredits
	}
	if err := s.sendAgent(agent, protocol.Message{Type: "file_download", RequestID: requestID, Path: path, Size: s.cfg.MaxFileTransferBytes, Credits: credits}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	timer := time.NewTimer(s.cfg.FileTransferTimeout)
	defer timer.Stop()
	responseCommitted := false
	metadataReceived := false
	expectedSize := int64(-1)
	var total int64
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
				if metadataReceived || m.Size < 0 {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					if !responseCommitted {
						http.Error(w, "device reported invalid file metadata", http.StatusBadGateway)
					}
					return
				}
				if s.cfg.MaxFileTransferBytes > 0 && m.Size > s.cfg.MaxFileTransferBytes {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					http.Error(w, "file exceeds configured maximum", http.StatusRequestEntityTooLarge)
					return
				}
				metadataReceived = true
				expectedSize = m.Size
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
				w.Header().Set("Content-Length", int64String(m.Size))
			case "file_chunk":
				if !metadataReceived {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					http.Error(w, "device sent file data before metadata", http.StatusBadGateway)
					return
				}
				b, err := base64.StdEncoding.DecodeString(m.Data)
				if err != nil {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					if !responseCommitted {
						http.Error(w, "device sent an invalid file chunk", http.StatusBadGateway)
					}
					return
				}
				if s.cfg.MaxFileTransferBytes > 0 && int64(len(b)) > s.cfg.MaxFileTransferBytes-total {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					if !responseCommitted {
						http.Error(w, "file exceeds configured maximum", http.StatusRequestEntityTooLarge)
					}
					return
				}
				total += int64(len(b))
				responseCommitted = true
				if _, err := w.Write(b); err != nil {
					_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				if agent.fileDownloadCredits {
					if err := s.sendAgent(agent, protocol.Message{Type: "file_credit", RequestID: requestID, Credits: 1}); err != nil {
						return
					}
				}
			case "file_end":
				if m.Error != "" && !responseCommitted {
					http.Error(w, m.Error, http.StatusBadGateway)
				}
				if m.Error == "" && (!metadataReceived || total != expectedSize) {
					if !responseCommitted {
						http.Error(w, "device sent an incomplete file", http.StatusBadGateway)
					}
				}
				return
			}
		case <-p.done:
			if !responseCommitted {
				http.Error(w, p.failure().Error(), http.StatusBadGateway)
			}
			return
		case <-timer.C:
			_ = s.sendAgent(agent, protocol.Message{Type: "file_cancel", RequestID: requestID})
			if !responseCommitted {
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
	credits := 0
	if agent.fileUploadCredits {
		credits = initialFileUploadCredits
	}
	if err := s.sendAgent(agent, protocol.Message{Type: "file_upload_start", RequestID: requestID, Path: path, Size: r.ContentLength, Mode: 0644, Credits: credits}); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	completed := false
	defer func() {
		if !completed {
			_ = s.sendAgent(agent, protocol.Message{Type: "file_upload_abort", RequestID: requestID})
		}
	}()

	wait := func(expected string) (protocol.Message, error) {
		t := time.NewTimer(s.cfg.FileTransferTimeout)
		defer t.Stop()
		for {
			select {
			case m := <-p.ch:
				if m.Type == expected {
					return m, nil
				}
				if m.Type == "file_upload_result" && m.Error != "" {
					return m, errors.New(m.Error)
				}
			case <-p.done:
				return protocol.Message{}, p.failure()
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
	uploadWindow := 0
	if agent.fileUploadCredits {
		if ready.Credits < 1 || ready.Credits > initialFileUploadCredits {
			http.Error(w, "device returned invalid upload credits", http.StatusBadGateway)
			return
		}
		credits = ready.Credits
		uploadWindow = ready.Credits
	}

	buf := make([]byte, s.cfg.FileTransferChunkBytes)
	var total int64
	for {
		if agent.fileUploadCredits && credits == 0 {
			credit, err := wait("file_upload_credit")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if credit.Credits < 1 || credit.Credits > uploadWindow {
				http.Error(w, "device returned invalid upload credits", http.StatusBadGateway)
				return
			}
			credits = min(uploadWindow, credits+credit.Credits)
		}
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
			if agent.fileUploadCredits {
				credits--
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
	for agent.fileUploadCredits && credits < uploadWindow {
		credit, err := wait("file_upload_credit")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if credit.Credits < 1 || credit.Credits > uploadWindow {
			http.Error(w, "device returned invalid upload credits", http.StatusBadGateway)
			return
		}
		credits = min(uploadWindow, credits+credit.Credits)
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
	completed = true
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": total})
}

func int64String(v int64) string { return strconv.FormatInt(v, 10) }
