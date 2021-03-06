package workspace

import (
	"bytes"
	"context"
	"testing"

	cmdutil "github.com/leg100/etok/cmd/util"
	"github.com/leg100/etok/pkg/env"
	"github.com/leg100/etok/pkg/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceSelect(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantEnv *env.Env
		out     string
		err     bool
	}{
		{
			name:    "defaults",
			args:    []string{"networking"},
			wantEnv: &env.Env{Namespace: "default", Workspace: "networking"},
			out:     "Current workspace now: default/networking\n",
		},
		{
			name:    "with explicit namespace",
			args:    []string{"networking", "--namespace", "dev"},
			wantEnv: &env.Env{Namespace: "dev", Workspace: "networking"},
			out:     "Current workspace now: dev/networking\n",
		},
	}

	for _, tt := range tests {
		testutil.Run(t, tt.name, func(t *testutil.T) {
			path := t.NewTempDir().Chdir().Root()

			out := new(bytes.Buffer)

			f := cmdutil.NewFakeFactory(out)

			cmd := selectCmd(f)
			cmd.SetArgs(tt.args)
			cmd.SetOut(f.Out)

			t.CheckError(tt.err, cmd.ExecuteContext(context.Background()))

			assert.Equal(t, tt.out, out.String())

			// Confirm .terraform/environment was written with expected contents
			etokenv, err := env.Read(path)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnv, etokenv)
		})
	}
}
