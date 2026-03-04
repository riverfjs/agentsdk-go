package api

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/riverfjs/agentsdk-go/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestBuiltinToolsAllowAdditionalDirectories(t *testing.T) {
	root := mustEvalSymlinks(t, t.TempDir())
	extra := mustEvalSymlinks(t, t.TempDir())
	filePath := filepath.Join(extra, "allowed.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello\n"), 0o600))
	filePath = mustEvalSymlinks(t, filePath)

	settings := &config.Settings{
		Permissions: &config.PermissionsConfig{
			AdditionalDirectories: []string{extra},
		},
	}
	factories := builtinToolFactories(root, false, EntryPointCLI, settings, nil, nil, nil)

	readRes, err := factories["file_read"]().Execute(context.Background(), map[string]interface{}{
		"file_path": filePath,
	})
	require.NoError(t, err)
	require.NotNil(t, readRes)

	globRes, err := factories["glob"]().Execute(context.Background(), map[string]interface{}{
		"pattern": "*.txt",
		"path":    extra,
	})
	require.NoError(t, err)
	require.NotNil(t, globRes)
}

func TestBuiltinToolsDenyOutsideRootWithoutAdditionalDirectories(t *testing.T) {
	root := mustEvalSymlinks(t, t.TempDir())
	outside := mustEvalSymlinks(t, t.TempDir())
	filePath := filepath.Join(outside, "denied.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello\n"), 0o600))
	filePath = mustEvalSymlinks(t, filePath)

	factories := builtinToolFactories(root, false, EntryPointCLI, &config.Settings{}, nil, nil, nil)

	_, err := factories["file_read"]().Execute(context.Background(), map[string]interface{}{
		"file_path": filePath,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "path not in sandbox allowlist")

	_, err = factories["glob"]().Execute(context.Background(), map[string]interface{}{
		"pattern": "*.txt",
		"path":    outside,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "path not in sandbox allowlist")
}

func TestBuiltinToolsDoNotExpandHomePathInAdditionalDirectories(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NotEmpty(t, home)

	extra, err := os.MkdirTemp(home, "agentsdk-home-")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(extra)
	})

	filePath := filepath.Join(extra, "home-allowed.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("hello\n"), 0o600))
	filePath = mustEvalSymlinks(t, filePath)

	settings := &config.Settings{
		Permissions: &config.PermissionsConfig{
			AdditionalDirectories: []string{"~/" + filepath.Base(extra)},
		},
	}
	factories := builtinToolFactories(mustEvalSymlinks(t, t.TempDir()), false, EntryPointCLI, settings, nil, nil, nil)

	readRes, err := factories["file_read"]().Execute(context.Background(), map[string]interface{}{
		"file_path": filePath,
	})
	require.Nil(t, readRes)
	require.Error(t, err)
	require.ErrorContains(t, err, "path not in sandbox allowlist")
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}
