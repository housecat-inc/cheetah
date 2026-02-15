package code_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/cheetah/pkg/code"
)

func TestCode(t *testing.T) {
	tests := []struct {
		_name string
		cmds  map[string]code.CmdResult
		env   []string
		err   string
		out   code.Out
	}{
		{
			_name: "space",
			env:   []string{"SPACE=s"},
			out:   code.Out{Dir: "/app", Name: "s"},
		},
		{
			_name: "conductor space",
			env:   []string{"CONDUCTOR_SPACE=c"},
			out:   code.Out{Dir: "/app", Name: "c"},
		},
		{
			_name: "branch",
			cmds: map[string]code.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Out: "feature/foo"},
			},
			out: code.Out{Dir: "/app", Name: "feature-foo"},
		},
		{
			_name: "error",
			cmds: map[string]code.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Err: "exit status 128"},
			},
			err: "set SPACE or CONDUCTOR_SPACE env var, or run in a git repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			cfg := code.TestConfig(tt.cmds, tt.out.Dir, tt.env)
			out, err := code.Code(cfg)
			if tt.err != "" {
				a.ErrorContains(err, tt.err)
				return
			}
			a.NoError(err)
			a.Equal(tt.out, out)
		})
	}
}
