package session

import (
	"database/sql"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestServiceScopesListLastAndGetToWorkingDir(t *testing.T) {
	t.Parallel()
	defer db.ResetPool()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)

	queries := db.New(conn)
	projectA := t.TempDir()
	projectB := t.TempDir()
	serviceA := NewService(queries, conn, projectA)
	serviceB := NewService(queries, conn, projectB)

	sessionA, err := serviceA.Create(t.Context(), "project-a", ModeExecute)
	require.NoError(t, err)
	sessionB, err := serviceB.Create(t.Context(), "project-b", ModeExecute)
	require.NoError(t, err)

	listA, err := serviceA.List(t.Context())
	require.NoError(t, err)
	require.Len(t, listA, 1)
	require.Equal(t, sessionA.ID, listA[0].ID)
	require.Equal(t, projectA, listA[0].WorkingDir)

	lastA, err := serviceA.GetLast(t.Context())
	require.NoError(t, err)
	require.Equal(t, sessionA.ID, lastA.ID)

	_, err = serviceA.Get(t.Context(), sessionB.ID)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
