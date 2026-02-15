package deps_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/spacecat/pkg/deps"
)

func TestSync(t *testing.T) {
	tests := []struct {
		_name string
		cmds  map[string]deps.CmdResult
		err   string
	}{
		{
			_name: "success",
		},
		{
			_name: "tidy fails",
			cmds: map[string]deps.CmdResult{
				"go mod tidy": {Err: "exit status 1"},
			},
			err: "go mod tidy",
		},
		{
			_name: "generate fails",
			cmds: map[string]deps.CmdResult{
				"go generate ./...": {Err: "exit status 1"},
			},
			err: "go generate",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			cfg := deps.TestConfig(tt.cmds)
			err := deps.Sync(cfg)
			if tt.err != "" {
				a.ErrorContains(err, tt.err)
				return
			}
			a.NoError(err)
		})
	}
}
