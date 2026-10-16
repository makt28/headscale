package hscontrol

// Linbei fork: DERP mesh registry for SingR nodes.
//
// Every SingR proxy node runs a DERP relay; all of them form one DERP region
// and mesh with each other. This registry is how they find each other without
// anyone maintaining a list:
//
//   - POST   /linbei/derp/register  a node announces (and keeps announcing,
//     as a heartbeat) its DERP port and certificate pin; its address is the
//     request's client IP unless it advertises one.
//   - DELETE /linbei/derp/register  a node leaves (singr derp disable).
//   - GET    /linbei/derp/mesh      {"psk", "nodes": [{"host", "pin"}]} — the
//     mesh PSK and every node heard from within the expiry window.
//
// All three require the node token, which is NOT a headscale API key: it can
// do nothing but these three calls, so a compromised proxy node cannot touch
// users, nodes or keys. The PSK is generated here on first use and never
// leaves this endpoint; tailnet clients never see it (they learn DERP from the
// static DERP map, see linbei/derp-map.yaml).
//
// State (PSK + nodes) is a small JSON file written atomically, not a database
// table, so the fork adds no migration.
//
// Configuration (absent node token = feature off):
//
//	linbei:
//	  derp_mesh:
//	    node_token_path: /etc/headscale/derp_node_token   # or node_token: "..."
//	    state_path: /var/lib/headscale/linbei_derp_mesh.json
//	    expiry: 24h

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/juanfont/headscale/hscontrol/util"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

const (
	linbeiDERPMeshDefaultExpiry = 24 * time.Hour
	linbeiDERPMeshMinTokenLen   = 32
	linbeiDERPMeshMaxBody       = 4 << 10
	linbeiDERPMeshMaxNodes      = 10000
	// Heartbeats only move last_seen; write it out at most this often.
	linbeiDERPMeshSaveInterval = 10 * time.Minute
)

type linbeiDERPMesh struct {
	token  []byte
	path   string
	expiry time.Duration
	now    func() time.Time

	mu       sync.Mutex
	state    linbeiDERPMeshState
	lastSave time.Time
}

type linbeiDERPMeshState struct {
	PSK   string                     `json:"psk"`
	Nodes map[string]*linbeiDERPNode `json:"nodes"`
}

type linbeiDERPNode struct {
	Host      string    `json:"host"`
	Port      uint16    `json:"port"`
	Pin       string    `json:"pin"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

func (n *linbeiDERPNode) address() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(int(n.Port)))
}

// mountLinbeiDERPMesh registers the registry routes when the feature is
// configured. A configured but broken setup is fatal: running without the
// registry would silently leave every DERP node unmeshed.
func (h *Headscale) mountLinbeiDERPMesh(r chi.Router) {
	mesh, err := newLinbeiDERPMeshFromConfig(h.cfg.NoisePrivateKeyPath)
	if err != nil {
		log.Fatal().Err(err).Msg("linbei.derp_mesh")
	}

	if mesh == nil {
		return
	}

	r.Post("/linbei/derp/register", mesh.handleRegister)
	r.Delete("/linbei/derp/register", mesh.handleLeave)
	r.Get("/linbei/derp/mesh", mesh.handleMesh)

	log.Info().
		Str("state_path", mesh.path).
		Stringer("expiry", mesh.expiry).
		Msg("linbei DERP mesh registry enabled")
}

func newLinbeiDERPMeshFromConfig(noiseKeyPath string) (*linbeiDERPMesh, error) {
	token := strings.TrimSpace(viper.GetString("linbei.derp_mesh.node_token"))

	if path := viper.GetString("linbei.derp_mesh.node_token_path"); path != "" {
		if token != "" {
			return nil, errors.New("set node_token or node_token_path, not both")
		}

		content, err := os.ReadFile(util.AbsolutePathFromConfigPath(path))
		if err != nil {
			return nil, fmt.Errorf("reading node_token_path: %w", err)
		}

		token = strings.TrimSpace(string(content))
		if token == "" {
			return nil, errors.New("node_token_path is empty")
		}
	}

	if token == "" {
		return nil, nil //nolint:nilnil // feature off
	}

	if len(token) < linbeiDERPMeshMinTokenLen {
		return nil, fmt.Errorf("node token must be at least %d characters", linbeiDERPMeshMinTokenLen)
	}

	path := viper.GetString("linbei.derp_mesh.state_path")
	if path == "" {
		path = filepath.Join(filepath.Dir(noiseKeyPath), "linbei_derp_mesh.json")
	} else {
		path = util.AbsolutePathFromConfigPath(path)
	}

	expiry := viper.GetDuration("linbei.derp_mesh.expiry")
	if expiry == 0 {
		expiry = linbeiDERPMeshDefaultExpiry
	}

	if expiry < time.Hour {
		return nil, errors.New("expiry must be at least 1h (nodes heartbeat every few minutes)")
	}

	return newLinbeiDERPMesh(token, path, expiry, time.Now)
}

func newLinbeiDERPMesh(
	token, path string,
	expiry time.Duration,
	now func() time.Time,
) (*linbeiDERPMesh, error) {
	mesh := &linbeiDERPMesh{
		token:  []byte(token),
		path:   path,
		expiry: expiry,
		now:    now,
		state:  linbeiDERPMeshState{Nodes: map[string]*linbeiDERPNode{}},
	}

	content, err := os.ReadFile(path)

	switch {
	case err == nil:
		err = json.Unmarshal(content, &mesh.state)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		if mesh.state.Nodes == nil {
			mesh.state.Nodes = map[string]*linbeiDERPNode{}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if !validMeshPSK(mesh.state.PSK) {
		if mesh.state.PSK != "" {
			return nil, fmt.Errorf("%s: psk is not 64 hex digits", path)
		}

		psk := make([]byte, 32)

		_, err = rand.Read(psk)
		if err != nil {
			return nil, err
		}

		mesh.state.PSK = hex.EncodeToString(psk)

		err = mesh.saveLocked()
		if err != nil {
			return nil, err
		}
	}

	return mesh, nil
}

func validMeshPSK(psk string) bool {
	if len(psk) != 64 || strings.ToLower(psk) != psk {
		return false
	}

	_, err := hex.DecodeString(psk)

	return err == nil
}

func (m *linbeiDERPMesh) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")

	return ok && subtle.ConstantTimeCompare([]byte(token), m.token) == 1
}

type linbeiRegisterRequest struct {
	// Host the other nodes should dial; empty means the request's client IP.
	Host string `json:"host"`
	Port uint16 `json:"port"`
	// Pin is base64(SHA-256(PKIX public key)) of the node's DERP certificate.
	Pin string `json:"pin"`
}

func (m *linbeiDERPMesh) readRequest(
	w http.ResponseWriter,
	r *http.Request,
	needPin bool,
) (linbeiRegisterRequest, string, bool) {
	var req linbeiRegisterRequest

	if !m.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return req, "", false
	}

	err := json.NewDecoder(io.LimitReader(r.Body, linbeiDERPMeshMaxBody)).Decode(&req)
	if err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)

		return req, "", false
	}

	if req.Port == 0 {
		http.Error(w, "port required", http.StatusBadRequest)

		return req, "", false
	}

	host, err := normalizeMeshHost(req.Host, r.RemoteAddr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return req, "", false
	}

	if needPin && !validMeshPin(req.Pin) {
		http.Error(w, "pin must be base64(sha256(public key))", http.StatusBadRequest)

		return req, "", false
	}

	return req, host, true
}

// normalizeMeshHost returns the advertised host, or the client IP when none
// was advertised.
func normalizeMeshHost(advertised, remoteAddr string) (string, error) {
	host := strings.TrimSpace(advertised)
	if host == "" {
		host = remoteAddr
		if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
			host = h
		}
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", errors.New("zoned address not allowed")
		}

		return addr.Unmap().String(), nil
	}

	if !validHostname(host) {
		return "", fmt.Errorf("invalid host %q", host)
	}

	return strings.ToLower(strings.TrimSuffix(host, ".")), nil
}

func validHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}

	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, c := range label {
			alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
			if !alnum && c != '-' {
				return false
			}
		}
	}

	return true
}

func validMeshPin(pin string) bool {
	decoded, err := base64.StdEncoding.DecodeString(pin)

	return err == nil && len(decoded) == sha256.Size
}

func (m *linbeiDERPMesh) handleRegister(w http.ResponseWriter, r *http.Request) {
	req, host, ok := m.readRequest(w, r, true)
	if !ok {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.pruneLocked(now)

	key := net.JoinHostPort(host, strconv.Itoa(int(req.Port)))
	node, exists := m.state.Nodes[key]

	if !exists && len(m.state.Nodes) >= linbeiDERPMeshMaxNodes {
		http.Error(w, "too many nodes", http.StatusInsufficientStorage)

		return
	}

	changed := !exists || node.Pin != req.Pin
	if !exists {
		node = &linbeiDERPNode{Host: host, Port: req.Port, FirstSeen: now}
		m.state.Nodes[key] = node

		log.Info().Str("node", key).Msg("linbei DERP mesh: node joined")
	} else if node.Pin != req.Pin {
		log.Info().Str("node", key).Msg("linbei DERP mesh: node certificate changed")
	}

	node.Pin = req.Pin
	node.LastSeen = now

	if changed || now.Sub(m.lastSave) >= linbeiDERPMeshSaveInterval {
		err := m.saveLocked()
		if err != nil {
			log.Error().Err(err).Msg("linbei DERP mesh: saving state")
			http.Error(w, "saving state failed", http.StatusInternalServerError)

			return
		}
	}

	writeMeshJSON(w, map[string]string{"host": key})
}

func (m *linbeiDERPMesh) handleLeave(w http.ResponseWriter, r *http.Request) {
	req, host, ok := m.readRequest(w, r, false)
	if !ok {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := net.JoinHostPort(host, strconv.Itoa(int(req.Port)))
	if _, exists := m.state.Nodes[key]; exists {
		delete(m.state.Nodes, key)

		err := m.saveLocked()
		if err != nil {
			log.Error().Err(err).Msg("linbei DERP mesh: saving state")
			http.Error(w, "saving state failed", http.StatusInternalServerError)

			return
		}

		log.Info().Str("node", key).Msg("linbei DERP mesh: node left")
	}

	w.WriteHeader(http.StatusNoContent)
}

type linbeiMeshResponse struct {
	PSK   string           `json:"psk"`
	Nodes []linbeiMeshNode `json:"nodes"`
}

type linbeiMeshNode struct {
	Host string `json:"host"`
	Pin  string `json:"pin"`
}

func (m *linbeiDERPMesh) handleMesh(w http.ResponseWriter, r *http.Request) {
	if !m.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	m.mu.Lock()
	m.pruneLocked(m.now())

	resp := linbeiMeshResponse{PSK: m.state.PSK, Nodes: []linbeiMeshNode{}}
	for _, node := range m.state.Nodes {
		resp.Nodes = append(resp.Nodes, linbeiMeshNode{Host: node.address(), Pin: node.Pin})
	}
	m.mu.Unlock()

	slices.SortFunc(resp.Nodes, func(a, b linbeiMeshNode) int { return strings.Compare(a.Host, b.Host) })

	w.Header().Set("Cache-Control", "no-store")
	writeMeshJSON(w, resp)
}

// pruneLocked drops nodes not heard from within the expiry window. Callers
// persist the result on their next save.
func (m *linbeiDERPMesh) pruneLocked(now time.Time) {
	for key, node := range m.state.Nodes {
		if now.Sub(node.LastSeen) > m.expiry {
			delete(m.state.Nodes, key)
			log.Info().Str("node", key).Msg("linbei DERP mesh: node expired")
		}
	}
}

func (m *linbeiDERPMesh) saveLocked() error {
	content, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(m.path), 0o700)
	if err != nil {
		return err
	}

	tmp := m.path + ".tmp"

	err = os.WriteFile(tmp, content, 0o600)
	if err == nil {
		err = os.Rename(tmp, m.path)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return err
	}

	m.lastSave = m.now()

	return nil
}

func writeMeshJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	_ = json.NewEncoder(w).Encode(v)
}
