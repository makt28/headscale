package hscontrol

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// Linbei fork: tests for linbei_admin_key.go.
func TestLinbeiAdminKeyConfig(t *testing.T) {
	t.Cleanup(func() { viper.Set("linbei.admin_key_path", nil) })

	key, err := linbeiAdminKeyFromConfig()
	require.NoError(t, err)
	require.Nil(t, key, "not configured = off")

	dir := t.TempDir()
	viper.Set("linbei.admin_key_path", filepath.Join(dir, "missing"))
	_, err = linbeiAdminKeyFromConfig()
	require.Error(t, err, "configured but missing must refuse to start")

	path := filepath.Join(dir, "admin_key")
	require.NoError(t, os.WriteFile(path, []byte("short\n"), 0o600))
	viper.Set("linbei.admin_key_path", path)
	_, err = linbeiAdminKeyFromConfig()
	require.Error(t, err, "too short")

	require.NoError(t, os.WriteFile(path, []byte("  0123456789abcdef0123456789abcdef\n"), 0o600))
	key, err = linbeiAdminKeyFromConfig()
	require.NoError(t, err)
	require.Equal(t, []byte("0123456789abcdef0123456789abcdef"), key, "trimmed")
}
