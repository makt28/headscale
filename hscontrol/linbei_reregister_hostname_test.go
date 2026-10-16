package hscontrol

import (
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// Linbei fork (linbeiRederiveGivenName): a client that renames itself and
// re-registers with a fresh auth key must get the new DNS name, unless an
// admin renamed the node. Reproduces 2026-10-17: Hostname "nec", Name stuck on
// "desktop-gk2q71f".
func TestLinbeiReregisterWithNewHostnameRederivesGivenName(t *testing.T) {
	app := createTestApp(t)
	user := app.state.CreateUserForTest("lb-1")
	machineKey := key.NewMachine()
	nodeKey := key.NewNode()

	register := func(hostname string) types.NodeView {
		t.Helper()
		// Our client asks sspapi for a new one-time key each time (tailnet.php join).
		pak, err := app.state.CreatePreAuthKey(user.TypedID(), false, false, nil, nil)
		require.NoError(t, err)
		resp, err := app.handleRegisterWithAuthKey(tailcfg.RegisterRequest{
			Auth:     &tailcfg.RegisterResponseAuth{AuthKey: pak.Key},
			NodeKey:  nodeKey.Public(),
			Hostinfo: &tailcfg.Hostinfo{Hostname: hostname},
			Expiry:   time.Now().Add(24 * time.Hour),
		}, machineKey.Public())
		require.NoError(t, err)
		require.True(t, resp.MachineAuthorized)
		nodes := app.state.ListNodesByUser(types.UserID(user.ID))
		require.Equal(t, 1, nodes.Len(), "re-registration must update the node in place")

		return nodes.At(0)
	}

	node := register("desktop-gk2q71f")
	require.Equal(t, "desktop-gk2q71f", node.GivenName())

	node = register("nec")
	require.Equal(t, "nec", node.Hostname())
	require.Equal(t, "nec", node.GivenName(), "renamed device keeps its old DNS name")

	// Re-registering with the same hostname (every restart) changes nothing
	node = register("nec")
	require.Equal(t, "nec", node.GivenName())

	// An admin rename is kept, the hostname still follows the client
	_, _, err := app.state.RenameNode(node.ID(), "office-pc")
	require.NoError(t, err)
	node = register("laptop")
	require.Equal(t, "laptop", node.Hostname())
	require.Equal(t, "office-pc", node.GivenName(), "admin rename must survive a client rename")
}

// A re-derived name that collides with another node's name is bumped like any
// other GivenName change, not rejected.
func TestLinbeiReregisterRenameCollisionIsBumped(t *testing.T) {
	app := createTestApp(t)
	user := app.state.CreateUserForTest("lb-1")

	// Register through the real path: CreateRegisteredNodeForTest only writes the
	// database, so the NodeStore collision check would not see that node.
	register := func(machineKey key.MachinePrivate, nodeKey key.NodePrivate, hostname string) types.NodeView {
		t.Helper()
		pak, err := app.state.CreatePreAuthKey(user.TypedID(), false, false, nil, nil)
		require.NoError(t, err)
		_, err = app.handleRegisterWithAuthKey(tailcfg.RegisterRequest{
			Auth:     &tailcfg.RegisterResponseAuth{AuthKey: pak.Key},
			NodeKey:  nodeKey.Public(),
			Hostinfo: &tailcfg.Hostinfo{Hostname: hostname},
			Expiry:   time.Now().Add(24 * time.Hour),
		}, machineKey.Public())
		require.NoError(t, err)
		node, ok := app.state.GetNodeByNodeKey(nodeKey.Public())
		require.True(t, ok)

		return node
	}

	register(key.NewMachine(), key.NewNode(), "nas")
	machineKey, nodeKey := key.NewMachine(), key.NewNode()
	register(machineKey, nodeKey, "desktop-abc")
	node := register(machineKey, nodeKey, "nas")
	require.Equal(t, "nas", node.Hostname())
	require.Equal(t, "nas-1", node.GivenName())
}
