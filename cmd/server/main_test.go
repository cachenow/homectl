package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedTerminalAssetsAreLocalAndServed(t *testing.T) {
	webRoot, err := fs.Sub(embedded, "web")
	if err != nil {
		t.Fatal(err)
	}

	assets := map[string]int{
		"vendor/xterm/xterm-6.0.0.mjs":              300_000,
		"vendor/xterm/addon-fit-0.11.0.mjs":         1_000,
		"vendor/xterm/xterm-6.0.0.css":              5_000,
		"vendor/xterm/LICENSE.xterm-6.0.0.txt":      1_000,
		"vendor/xterm/LICENSE.addon-fit-0.11.0.txt": 1_000,
		"vendor/xterm/README.md":                    100,
	}
	for name, minimumSize := range assets {
		data, readErr := fs.ReadFile(webRoot, name)
		if readErr != nil {
			t.Errorf("read embedded %s: %v", name, readErr)
			continue
		}
		if len(data) < minimumSize {
			t.Errorf("embedded %s has %d bytes, want at least %d", name, len(data), minimumSize)
		}
	}

	index, err := fs.ReadFile(webRoot, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	if strings.Contains(page, "cdn.jsdelivr.net") {
		t.Fatal("index.html still references jsDelivr")
	}
	for _, localReference := range []string{
		"/vendor/xterm/xterm-6.0.0.css",
		"/vendor/xterm/xterm-6.0.0.mjs",
		"/vendor/xterm/addon-fit-0.11.0.mjs",
	} {
		if !strings.Contains(page, localReference) {
			t.Errorf("index.html does not reference %s", localReference)
		}
	}

	handler := http.FileServer(http.FS(webRoot))
	for path, contentType := range map[string]string{
		"/vendor/xterm/xterm-6.0.0.mjs": "javascript",
		"/vendor/xterm/xterm-6.0.0.css": "text/css",
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("GET %s returned %d", path, rr.Code)
		} else if got := rr.Header().Get("Content-Type"); !strings.Contains(got, contentType) {
			t.Errorf("GET %s Content-Type=%q, want %q", path, got, contentType)
		}
	}
}
