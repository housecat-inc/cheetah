package port

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/housecat-inc/spacecat/pkg/api"
)

type Config struct {
	CheckHealth   func(port int) (int, error)
	CheckInterval time.Duration
	CheckRetries  int
	ReportHealth  func(status string, port int)
}

type Manager struct {
	active int
	blue   int
	config Config
	green  int
	mu     sync.Mutex
}

func New(blue, green int, cfg Config) *Manager {
	return &Manager{
		active: blue,
		blue:   blue,
		config: cfg,
		green:  green,
	}
}

func DefaultConfig(client *api.Client, space string) Config {
	return Config{
		CheckHealth: func(port int) (int, error) {
			c := &http.Client{Timeout: 1 * time.Second}
			resp, err := c.Get(fmt.Sprintf("http://localhost:%d/health", port))
			if err != nil {
				return 0, err
			}
			resp.Body.Close()
			return resp.StatusCode, nil
		},
		CheckInterval: 500 * time.Millisecond,
		CheckRetries:  30,
		ReportHealth: func(status string, port int) {
			client.HealthUpdate(space, port, status)
		},
	}
}

func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func (m *Manager) Inactive() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == m.green {
		return m.blue
	}
	return m.green
}

func (m *Manager) SetActive(port int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = port
}

func (m *Manager) WaitForHealthy(port int) bool {
	for i := 0; i < m.config.CheckRetries; i++ {
		time.Sleep(m.config.CheckInterval)
		status, err := m.config.CheckHealth(port)
		if err == nil && status == http.StatusOK {
			return true
		}
	}
	return false
}

func (m *Manager) ReportHealth(status string) {
	m.mu.Lock()
	port := m.active
	m.mu.Unlock()
	m.config.ReportHealth(status, port)
}

func (m *Manager) Swap(start func(int) error, stop func(int)) bool {
	inactive := m.Inactive()
	old := m.Active()

	if err := start(inactive); err != nil {
		return false
	}

	if !m.WaitForHealthy(inactive) {
		stop(inactive)
		return false
	}

	m.SetActive(inactive)
	m.ReportHealth("healthy")
	stop(old)
	return true
}
