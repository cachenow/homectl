package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedWebIncludesLocalTerminalAssets(t *testing.T) {
	webRoot, err := fs.Sub(embedded, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"index.html",
		"vendor/xterm/xterm-6.0.0.css",
		"vendor/xterm/xterm-6.0.0.mjs",
		"vendor/xterm/addon-fit-0.11.0.mjs",
		"vendor/xterm/LICENSE.xterm-6.0.0",
		"vendor/xterm/LICENSE.addon-fit-0.11.0",
	} {
		contents, err := fs.ReadFile(webRoot, path)
		if err != nil || len(contents) == 0 {
			t.Fatalf("embedded asset %q: bytes=%d err=%v", path, len(contents), err)
		}
	}
	index, err := fs.ReadFile(webRoot, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "cdn.jsdelivr.net") {
		t.Fatal("web UI still references the terminal CDN")
	}
}

func TestEmbeddedModuleAssetsHaveJavaScriptContentType(t *testing.T) {
	webRoot, err := fs.Sub(embedded, "web")
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/vendor/xterm/xterm-6.0.0.mjs", nil)
	http.FileServer(http.FS(webRoot)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("module status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("module Content-Type=%q", contentType)
	}
}

func TestEmbeddedWebKeepsV2DashboardInteractionContract(t *testing.T) {
	index, err := fs.ReadFile(embedded, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(index)
	for _, fragment := range []string{
		`grid-template-columns:repeat(3,minmax(0,1fr))`,
		`.metric{display:flex;height:156px`,
		`@media(max-width:900px)`,
		`.metrics{grid-template-columns:1fr`,
		`const metricCardKeys=['cpu','memory','disk','network','processes','diskio']`,
		`class="copy-ip"`,
		`ghost.className='drag-ghost'`,
		`至少保留 3 张指标卡片`,
		`id="themeSystem"`,
		`id="mobileThemeSystem"`,
		`onclick="openAccount()"`,
		`onclick="deleteManagedDevice(`,
		`onclick="action(${idx},'reboot')"`,
		`onclick="action(${idx},'poweroff')"`,
		`detailHTML('Agent 版本'`,
	} {
		if !strings.Contains(page, fragment) {
			t.Errorf("dashboard contract fragment missing: %s", fragment)
		}
	}
}
