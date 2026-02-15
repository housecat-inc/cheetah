package port

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/assert"
)

func testConfig(checkHealth func(int) (int, error), reports *[]string) Config {
	return Config{
		CheckHealth:   checkHealth,
		CheckInterval: time.Millisecond,
		CheckRetries:  3,
		ReportHealth: func(status string, port int) {
			if reports != nil {
				*reports = append(*reports, fmt.Sprintf("%s:%d", status, port))
			}
		},
	}
}

func TestInactive(t *testing.T) {
	tests := []struct {
		_name  string
		active int
		blue   int
		green  int
		out    int
	}{
		{_name: "active blue returns green", blue: 5000, green: 5001, active: 5000, out: 5001},
		{_name: "active green returns blue", blue: 5000, green: 5001, active: 5001, out: 5000},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			cfg := testConfig(nil, nil)
			m := New(tt.blue, tt.green, cfg)
			m.SetActive(tt.active)
			a.Equal(tt.out, m.Inactive())
		})
	}
}

func TestWaitForHealthy(t *testing.T) {
	tests := []struct {
		_name       string
		checkHealth func(int) (int, error)
		out         bool
	}{
		{
			_name: "healthy immediately",
			checkHealth: func(int) (int, error) {
				return http.StatusOK, nil
			},
			out: true,
		},
		{
			_name: "never healthy",
			checkHealth: func(int) (int, error) {
				return 0, errors.New("connection refused")
			},
			out: false,
		},
		{
			_name: "non-200 status",
			checkHealth: func(int) (int, error) {
				return http.StatusServiceUnavailable, nil
			},
			out: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)
			cfg := testConfig(tt.checkHealth, nil)
			m := New(5000, 5001, cfg)
			a.Equal(tt.out, m.WaitForHealthy(5000))
		})
	}
}

func TestSwap(t *testing.T) {
	tests := []struct {
		_name      string
		healthy    bool
		out        bool
		startErr   error
		wantActive int
		wantReport []string
		wantStop   []int
	}{
		{
			_name:      "successful swap",
			healthy:    true,
			out:        true,
			wantActive: 5001,
			wantReport: []string{"healthy:5001"},
			wantStop:   []int{5000},
		},
		{
			_name:      "start fails",
			startErr:   errors.New("build error"),
			out:        false,
			wantActive: 5000,
		},
		{
			_name:      "health check fails",
			healthy:    false,
			out:        false,
			wantActive: 5000,
			wantStop:   []int{5001},
		},
	}
	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			var reports []string
			checkHealth := func(int) (int, error) {
				if tt.healthy {
					return http.StatusOK, nil
				}
				return 0, errors.New("unhealthy")
			}
			cfg := testConfig(checkHealth, &reports)
			m := New(5000, 5001, cfg)

			var stopped []int
			start := func(port int) error { return tt.startErr }
			stop := func(port int) { stopped = append(stopped, port) }

			result := m.Swap(start, stop)
			a.Equal(tt.out, result)
			a.Equal(tt.wantActive, m.Active())

			if tt.wantReport != nil {
				a.Equal(tt.wantReport, reports)
			}
			if tt.wantStop != nil {
				a.Equal(tt.wantStop, stopped)
			}
		})
	}
}

func TestReportHealth(t *testing.T) {
	a := assert.New(t)
	var reports []string
	cfg := testConfig(nil, &reports)
	m := New(5000, 5001, cfg)

	m.ReportHealth("unknown")
	m.SetActive(5001)
	m.ReportHealth("healthy")

	a.Equal([]string{"unknown:5000", "healthy:5001"}, reports)
}
