package browser_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/housecat-inc/cheetah/pkg/api"
)

const (
	testAppPort  = 60002
	testDashPort = 60000
	testPGPort   = 60001
)

func TestDashboardAndProxy(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", testDashPort), time.Second)
	if err == nil {
		conn.Close()
		t.Skipf("port %d in use", testDashPort)
	}

	a := assert.New(t)
	r := require.New(t)
	screenshots := t.ArtifactDir()

	bin := filepath.Join(t.TempDir(), "cheetah")
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/cheetah").CombinedOutput()
	r.NoError(err, string(out))

	proc := exec.Command(bin)
	proc.Dir = t.TempDir()
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	proc.Env = append(os.Environ(),
		fmt.Sprintf("APP_PORT=%d", testAppPort),
		fmt.Sprintf("PG_PORT=%d", testPGPort),
		fmt.Sprintf("PORT=%d", testDashPort),
	)
	r.NoError(proc.Start())
	t.Cleanup(func() {
		proc.Process.Signal(syscall.SIGTERM)
		proc.Wait()
	})

	dashURL := fmt.Sprintf("http://localhost:%d", testDashPort)
	waitForReady(t, dashURL+"/api/status")

	resp := registerApp(t, dashURL, "myapp")

	serveMockApp(t, resp.Ports.Blue, "v1")
	putHealth(t, dashURL, "myapp", "healthy", resp.Ports.Blue)

	browser := rod.New()
	r.NoError(browser.Connect())
	t.Cleanup(func() { browser.Close() })

	page := browser.MustPage()
	var mu sync.Mutex
	var consoleErrors []string
	go page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) {
		if e.Type == proto.RuntimeConsoleAPICalledTypeError {
			mu.Lock()
			for _, arg := range e.Args {
				if arg.Description != "" {
					consoleErrors = append(consoleErrors, arg.Description)
				}
			}
			mu.Unlock()
		}
	})()

	page.MustNavigate(dashURL).MustWaitStable()
	page.Timeout(5 * time.Second).MustElement("#app-table table")

	page.MustScreenshot(filepath.Join(screenshots, "dashboard-before.png"))

	tableText := page.MustElement("#app-table").MustText()
	a.Contains(tableText, "myapp")
	a.Contains(tableText, "healthy")

	serveMockApp(t, resp.Ports.Green, "v2")
	putHealth(t, dashURL, "myapp", "healthy", resp.Ports.Green)

	page.Timeout(5*time.Second).MustElementR(".active-port", fmt.Sprintf(":%d", resp.Ports.Green))

	page.MustScreenshot(filepath.Join(screenshots, "dashboard-after.png"))

	proxyPage := browser.MustPage(fmt.Sprintf("http://myapp.localhost:%d/", testDashPort))
	proxyPage.MustWaitStable()
	a.Contains(proxyPage.MustElement("body").MustText(), "v2")
	proxyPage.MustScreenshot(filepath.Join(screenshots, "proxy.png"))

	mu.Lock()
	a.Empty(consoleErrors)
	mu.Unlock()
}

func waitForReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", url)
}

func registerApp(t *testing.T, dashURL, space string) api.AppOut {
	t.Helper()
	body, _ := json.Marshal(api.AppIn{
		Config: []string{".envrc"},
		Dir:    t.TempDir(),
		Space:  space,
		Watch:  api.Watch{Match: []string{"*.go"}},
	})
	resp, err := http.Post(dashURL+"/api/apps", "application/json", bytes.NewReader(body))
	require.New(t).NoError(err)
	defer resp.Body.Close()
	require.New(t).Equal(http.StatusCreated, resp.StatusCode)
	var result api.AppOut
	require.New(t).NoError(json.NewDecoder(resp.Body).Decode(&result))
	return result
}

func serveMockApp(t *testing.T, port int, version string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<!DOCTYPE html><html><body><h1>%s</h1></body></html>", version)
	})
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.New(t).NoError(err)
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func putHealth(t *testing.T, dashURL, space, status string, portActive int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"port_active": portActive,
		"status":      status,
	})
	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/apps/%s/health", dashURL, space), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.New(t).NoError(err)
	resp.Body.Close()
}
