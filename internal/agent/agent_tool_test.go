package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAgentToolRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		role        string
		wantProfile scheduler.WorkerProfile
		wantRole    string
		wantErr     string
	}{
		{
			name:        "default explore",
			role:        "",
			wantProfile: scheduler.ProfileExploreAgent,
			wantRole:    config.AgentExplore,
		},
		{
			name:        "explore",
			role:        "explore",
			wantProfile: scheduler.ProfileExploreAgent,
			wantRole:    config.AgentExplore,
		},
		{
			name:        "worker",
			role:        "worker",
			wantProfile: scheduler.ProfileWorkerAgent,
			wantRole:    config.AgentWorker,
		},
		{
			name:    "invalid",
			role:    "brain",
			wantErr: `unknown agent role "brain"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			profile, role, err := resolveAgentToolRole(tt.role)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantProfile, profile)
			assert.Equal(t, tt.wantRole, role)
		})
	}
}
