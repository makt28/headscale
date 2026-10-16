package hscontrol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// Linbei fork: tests for linbei_derp_mesh.go.

const testMeshToken = "0123456789abcdef0123456789abcdef-node-token"

type meshTestClock struct{ t time.Time }

func (c *meshTestClock) now() time.Time { return c.t }

func testPin(seed string) string {
	sum := sha256.Sum256([]byte(seed))

	return base64.StdEncoding.EncodeToString(sum[:])
}

func newTestMesh(t *testing.T, path string, clock *meshTestClock) (*linbeiDERPMesh, http.Handler) {
	t.Helper()

	mesh, err := newLinbeiDERPMesh(testMeshToken, path, 24*time.Hour, clock.now)
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Post("/linbei/derp/register", mesh.handleRegister)
	r.Delete("/linbei/derp/register", mesh.handleLeave)
	r.Get("/linbei/derp/mesh", mesh.handleMesh)

	return mesh, r
}

func meshCall(
	t *testing.T,
	h http.Handler,
	method, remote, token string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader

	if body != nil {
		content, err := json.Marshal(body)
		require.NoError(t, err)

		reader = bytes.NewReader(content)
	} else {
		reader = bytes.NewReader(nil)
	}

	path := "/linbei/derp/register"
	if method == http.MethodGet {
		path = "/linbei/derp/mesh"
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, reader)
	req.RemoteAddr = remote

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func readMesh(t *testing.T, h http.Handler) linbeiMeshResponse {
	t.Helper()

	rec := meshCall(t, h, http.MethodGet, "198.51.100.9:1", testMeshToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp linbeiMeshResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	return resp
}

func TestLinbeiDERPMeshRequiresNodeToken(t *testing.T) {
	clock := &meshTestClock{t: time.Unix(1_800_000_000, 0)}
	_, h := newTestMesh(t, filepath.Join(t.TempDir(), "mesh.json"), clock)
	body := map[string]any{"port": 8443, "pin": testPin("a")}

	for _, token := range []string{"", "wrong", testMeshToken + "x", testMeshToken[:31]} {
		require.Equal(t, http.StatusUnauthorized,
			meshCall(t, h, http.MethodPost, "203.0.113.5:4000", token, body).Code, "register with %q", token)
		require.Equal(t, http.StatusUnauthorized,
			meshCall(t, h, http.MethodGet, "203.0.113.5:4000", token, nil).Code, "mesh with %q", token)
		require.Equal(t, http.StatusUnauthorized,
			meshCall(t, h, http.MethodDelete, "203.0.113.5:4000", token, body).Code, "leave with %q", token)
	}
}

func TestLinbeiDERPMeshRegisterListLeave(t *testing.T) {
	clock := &meshTestClock{t: time.Unix(1_800_000_000, 0)}
	path := filepath.Join(t.TempDir(), "mesh.json")
	_, h := newTestMesh(t, path, clock)

	// Address defaults to the client IP; an advertised host wins; IPv6 is
	// bracketed in the list.
	rec := meshCall(t, h, http.MethodPost, "203.0.113.5:40000", testMeshToken,
		map[string]any{"port": 8443, "pin": testPin("a")})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"203.0.113.5:8443"`)

	require.Equal(t, http.StatusOK, meshCall(t, h, http.MethodPost, "203.0.113.6:1", testMeshToken,
		map[string]any{"port": 9443, "pin": testPin("b"), "host": "JP1.Example.com."}).Code)
	require.Equal(t, http.StatusOK, meshCall(t, h, http.MethodPost, "[2001:db8::7]:1", testMeshToken,
		map[string]any{"port": 8443, "pin": testPin("c")}).Code)
	// A proxy that rewrote RemoteAddr to a bare IP is handled too.
	require.Equal(t, http.StatusOK, meshCall(t, h, http.MethodPost, "198.51.100.20", testMeshToken,
		map[string]any{"port": 8443, "pin": testPin("d")}).Code)

	resp := readMesh(t, h)
	require.Len(t, resp.PSK, 64)
	require.Equal(t, []linbeiMeshNode{
		{Host: "198.51.100.20:8443", Pin: testPin("d")},
		{Host: "203.0.113.5:8443", Pin: testPin("a")},
		{Host: "[2001:db8::7]:8443", Pin: testPin("c")},
		{Host: "jp1.example.com:9443", Pin: testPin("b")},
	}, resp.Nodes)

	// Re-registering with a new certificate replaces the pin.
	require.Equal(t, http.StatusOK, meshCall(t, h, http.MethodPost, "203.0.113.5:1", testMeshToken,
		map[string]any{"port": 8443, "pin": testPin("a2")}).Code)
	require.Equal(t, testPin("a2"), readMesh(t, h).Nodes[1].Pin)

	require.Equal(t, http.StatusNoContent, meshCall(t, h, http.MethodDelete, "203.0.113.6:1", testMeshToken,
		map[string]any{"port": 9443, "host": "jp1.example.com"}).Code)
	require.Len(t, readMesh(t, h).Nodes, 3)
}

func TestLinbeiDERPMeshRejectsBadInput(t *testing.T) {
	clock := &meshTestClock{t: time.Unix(1_800_000_000, 0)}
	_, h := newTestMesh(t, filepath.Join(t.TempDir(), "mesh.json"), clock)

	for name, body := range map[string]map[string]any{
		"no port":        {"pin": testPin("a")},
		"no pin":         {"port": 8443},
		"short pin":      {"port": 8443, "pin": "AAAA"},
		"not base64 pin": {"port": 8443, "pin": strings.Repeat("!", 44)},
		"url host":       {"port": 8443, "pin": testPin("a"), "host": "https://x.example.com"},
		"path host":      {"port": 8443, "pin": testPin("a"), "host": "x.example.com/derp"},
		"zoned host":     {"port": 8443, "pin": testPin("a"), "host": "fe80::1%eth0"},
		"underscore":     {"port": 8443, "pin": testPin("a"), "host": "bad_host.example.com"},
	} {
		rec := meshCall(t, h, http.MethodPost, "203.0.113.5:1", testMeshToken, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
	}

	require.Empty(t, readMesh(t, h).Nodes)
}

func TestLinbeiDERPMeshExpiresSilentNodes(t *testing.T) {
	clock := &meshTestClock{t: time.Unix(1_800_000_000, 0)}
	_, h := newTestMesh(t, filepath.Join(t.TempDir(), "mesh.json"), clock)

	meshCall(t, h, http.MethodPost, "203.0.113.5:1", testMeshToken, map[string]any{"port": 8443, "pin": testPin("a")})
	clock.t = clock.t.Add(20 * time.Hour)
	meshCall(t, h, http.MethodPost, "203.0.113.6:1", testMeshToken, map[string]any{"port": 8443, "pin": testPin("b")})

	clock.t = clock.t.Add(5 * time.Hour) // a: 25h silent, b: 5h
	resp := readMesh(t, h)
	require.Equal(t, []linbeiMeshNode{{Host: "203.0.113.6:8443", Pin: testPin("b")}}, resp.Nodes)

	// A heartbeat brings an expired node straight back.
	meshCall(t, h, http.MethodPost, "203.0.113.5:1", testMeshToken, map[string]any{"port": 8443, "pin": testPin("a")})
	require.Len(t, readMesh(t, h).Nodes, 2)
}

// The PSK and the registry survive a restart: a control-server restart must
// neither change the PSK (every node would drop out of the mesh) nor empty the
// list until every node heartbeats again.
func TestLinbeiDERPMeshPersistsAcrossRestart(t *testing.T) {
	clock := &meshTestClock{t: time.Unix(1_800_000_000, 0)}
	path := filepath.Join(t.TempDir(), "sub", "mesh.json")

	_, h := newTestMesh(t, path, clock)
	meshCall(t, h, http.MethodPost, "203.0.113.5:1", testMeshToken, map[string]any{"port": 8443, "pin": testPin("a")})
	before := readMesh(t, h)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "state holds the PSK")

	_, h2 := newTestMesh(t, path, clock)
	require.Equal(t, before, readMesh(t, h2))

	// A corrupted PSK is refused rather than silently replaced.
	require.NoError(t, os.WriteFile(path, []byte(`{"psk":"short","nodes":{}}`), 0o600))
	_, err = newLinbeiDERPMesh(testMeshToken, path, 24*time.Hour, clock.now)
	require.Error(t, err)
}

func TestLinbeiDERPMeshConfig(t *testing.T) {
	t.Cleanup(func() {
		for _, k := range []string{"node_token", "node_token_path", "state_path", "expiry"} {
			viperSetForTest(t, "linbei.derp_mesh."+k, nil)
		}
	})

	dir := t.TempDir()

	mesh, err := newLinbeiDERPMeshFromConfig(filepath.Join(dir, "noise.key"))
	require.NoError(t, err)
	require.Nil(t, mesh, "no token = feature off")

	viperSetForTest(t, "linbei.derp_mesh.node_token", "too-short")
	_, err = newLinbeiDERPMeshFromConfig(filepath.Join(dir, "noise.key"))
	require.Error(t, err)

	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(testMeshToken+"\n"), 0o600))
	viperSetForTest(t, "linbei.derp_mesh.node_token", nil)
	viperSetForTest(t, "linbei.derp_mesh.node_token_path", tokenPath)

	mesh, err = newLinbeiDERPMeshFromConfig(filepath.Join(dir, "noise.key"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "linbei_derp_mesh.json"), mesh.path, "state defaults next to the noise key")
	require.Equal(t, []byte(testMeshToken), mesh.token, "token file is trimmed")

	viperSetForTest(t, "linbei.derp_mesh.expiry", "10m")
	_, err = newLinbeiDERPMeshFromConfig(filepath.Join(dir, "noise.key"))
	require.Error(t, err, "expiry shorter than the heartbeat cadence")
}

// viperSetForTest sets (or, with nil, clears) a global viper key. Tests using
// it must not run in parallel.
func viperSetForTest(t *testing.T, key string, value any) {
	t.Helper()
	viper.Set(key, value)
}
