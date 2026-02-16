package code_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/housecat-inc/cheetah/pkg/code"
)

func TestAppName(t *testing.T) {
	tests := []struct {
		_name string
		dir   string
		out   string
		space string
	}{
		{
			_name: "simple",
			dir:   "/Users/noah/projects/cheetah",
			space: "main",
			out:   "cheetah",
		},
		{
			_name: "worktree matches space",
			dir:   "/Users/noah/conductor/workspaces/cheetah/victoria",
			space: "victoria",
			out:   "cheetah",
		},
		{
			_name: "worktree does not match space",
			dir:   "/Users/noah/conductor/workspaces/cheetah/victoria",
			space: "main",
			out:   "victoria",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			out := code.AppName(tt.dir, tt.space)
			a.Equal(tt.out, out)
		})
	}
}

func TestCode(t *testing.T) {
	tests := []struct {
		_name string
		cmds  map[string]code.CmdResult
		dir   string
		env   []string
		err   string
		out   code.Out
	}{
		{
			_name: "space explicit",
			dir:   "/projects/auth",
			env:   []string{"SPACE=s"},
			out:   code.Out{Dir: "/projects/auth", Name: "s"},
		},
		{
			_name: "conductor workspace name",
			dir:   "/workspaces/cheetah/caracas/apps/auth",
			env:   []string{"CONDUCTOR_WORKSPACE_NAME=caracas"},
			out:   code.Out{Dir: "/workspaces/cheetah/caracas/apps/auth", Name: "caracas"},
		},
		{
			_name: "branch combines with dir",
			dir:   "/projects/cheetah/apps/greet",
			cmds: map[string]code.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Out: "feature/foo"},
			},
			out: code.Out{Dir: "/projects/cheetah/apps/greet", Name: "greet-feature-foo"},
		},
		{
			_name: "branch main combines with dir",
			dir:   "/projects/cheetah",
			cmds: map[string]code.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Out: "main"},
			},
			out: code.Out{Dir: "/projects/cheetah", Name: "cheetah-main"},
		},
		{
			_name: "error",
			dir:   "/app",
			cmds: map[string]code.CmdResult{
				"git rev-parse --abbrev-ref HEAD": {Err: "exit status 128"},
			},
			err: "set SPACE or CONDUCTOR_WORKSPACE_NAME env var, or run in a git repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt._name, func(t *testing.T) {
			a := assert.New(t)

			cfg := code.TestConfig(tt.cmds, tt.dir, tt.env)
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
