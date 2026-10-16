package servertest_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/servertest"
	"github.com/juanfont/headscale/hscontrol/state"
	"github.com/stretchr/testify/require"
)

// Linbei fork: the fixed admin key (hscontrol/state/linbei_admin_key.go) works
// for exactly the calls sspapi's tailnet.php makes, and nothing else gets in.
// Not parallel with itself; other tests are unaffected by a key they never send.
func TestLinbeiAdminKeyDrivesTailnetJoinFlow(t *testing.T) {
	secret := make([]byte, 24)
	_, err := rand.Read(secret)
	require.NoError(t, err)

	key := hex.EncodeToString(secret)
	state.SetLinbeiAdminKey([]byte(key))

	srv := servertest.NewServer(t, servertest.WithRealListener())

	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()

		var reader *bytes.Reader
		if body != nil {
			content, err := json.Marshal(body)
			require.NoError(t, err)

			reader = bytes.NewReader(content)
		} else {
			reader = bytes.NewReader(nil)
		}

		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, reader)
		require.NoError(t, err)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer resp.Body.Close()

		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)

		return resp.StatusCode, out
	}

	for _, bad := range []string{"", "wrong", key + "x", key[:len(key)-1]} {
		code, _ := call(http.MethodGet, "/api/v1/user", bad, nil)
		require.Equal(t, http.StatusUnauthorized, code, "token %q", bad)
	}

	// Same order as tailnet.php: find/create user, list+revoke keys, mint key.
	code, out := call(http.MethodGet, "/api/v1/user?name=lb-7", key, nil)
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, out["users"])

	code, out = call(http.MethodPost, "/api/v1/user", key, map[string]any{"name": "lb-7"})
	require.Equal(t, http.StatusOK, code, out)
	userID := out["user"].(map[string]any)["id"].(string)

	mint := func() string {
		code, out := call(http.MethodPost, "/api/v1/preauthkey", key, map[string]any{
			"user":       userID,
			"expiration": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
		})
		require.Equal(t, http.StatusOK, code, out)

		return out["preAuthKey"].(map[string]any)["id"].(string)
	}
	first := mint()

	code, out = call(http.MethodGet, "/api/v2/tailnet/-/keys", key, nil)
	require.Equal(t, http.StatusOK, code, out)
	require.NotEmpty(t, out["keys"])

	code, _ = call(http.MethodDelete, "/api/v2/tailnet/-/keys/"+first, key, nil)
	require.Equal(t, http.StatusOK, code)
	mint()
}
