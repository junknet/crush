package remoteregistry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistry(t *testing.T) {
	dataDir := t.TempDir()
	r, err := NewRegistry(dataDir)
	require.NoError(t, err)

	mount := Mount{
		Host:       "test-host",
		RemotePath: "/remote",
		LocalPath:  "/local",
		MountedAt:  time.Now(),
	}

	err = r.AddMount(mount)
	require.NoError(t, err)

	mounts := r.ListMounts()
	assert.Len(t, mounts, 1)
	assert.Equal(t, "test-host", mounts[0].Host)

	// Persist test
	r2, err := NewRegistry(dataDir)
	require.NoError(t, err)
	mounts2 := r2.ListMounts()
	assert.Len(t, mounts2, 1)
	assert.Equal(t, "/remote", mounts2[0].RemotePath)

	err = r.RemoveMount("test-host", "/remote")
	require.NoError(t, err)
	assert.Len(t, r.ListMounts(), 0)
}
