package space_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/spacecat/pkg/space"
)

func TestSpace(t *testing.T) {
	tests := []struct {
		_name string
		cmds  map[string]space.CmdResult
		env   []string
		err   string
		out   space.Out
	}{
		{
			_name: "space",
			env:   []string{"SPACE=s"},
			out:   space.Out{Dir: "/app", Name: "s"},
		},
		{
			_name: "conductor space",
			env:   []string{"CONDUCTOR_SPACE=c"},
			out:   space.Out{Dir: "/app", Name: "c"},
		},
		{
			_name: "branch",
			cmds: map[string]space.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Out: "feature/foo"},
			},
			out: space.Out{Dir: "/app", Name: "feature-foo"},
		},
		{
			_name: "error",
			cmds: map[string]space.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Err: "exit status 128"},
			},
			err: "set SPACE or CONDUCTOR_SPACE env var, or run in a git repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			cfg := space.TestConfig(tt.cmds, tt.out.Dir, tt.env)
			out, err := space.Space(cfg)
			if tt.err != "" {
				a.ErrorContains(err, tt.err)
				return
			}
			a.NoError(err)
			a.Equal(tt.out, out)
		})
	}
}
