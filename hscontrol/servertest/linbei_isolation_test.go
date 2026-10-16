package servertest_test

import (
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/servertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Linbei fork: users never see each other's nodes, whatever the policy
// (hscontrol/state/linbei_isolation.go). Upstream tests whose premise is
// cross-user visibility call skipCrossUser; the end-to-end promise and the
// delta-removal path they also exercised are covered here, within one user.

func skipCrossUser(t *testing.T) {
	t.Helper()
	t.Skip("Linbei fork: users never see each other's nodes; see TestLinbeiUsersIsolated")
}

func TestLinbeiUsersIsolated(t *testing.T) {
	t.Parallel()

	// No policy = upstream's allow-all, the case isolation exists for.
	srv := servertest.NewServer(t)
	alice := srv.CreateUser(t, "lb-alice")
	bob := srv.CreateUser(t, "lb-bob")

	a1 := servertest.NewClient(t, srv, "lb-alice-1", servertest.WithUser(alice))
	a2 := servertest.NewClient(t, srv, "lb-alice-2", servertest.WithUser(alice))
	b1 := servertest.NewClient(t, srv, "lb-bob-1", servertest.WithUser(bob))

	a1.WaitForPeers(t, 1, 15*time.Second)
	a2.WaitForPeers(t, 1, 15*time.Second)

	_, found := a1.PeerByName("lb-alice-2")
	assert.True(t, found, "alice's devices see each other")

	// Give any cross-user update time to arrive before asserting its absence.
	time.Sleep(3 * time.Second)

	for _, c := range []*servertest.TestClient{a1, a2} {
		nm := c.Netmap()
		require.NotNil(t, nm)
		assert.Len(t, nm.Peers, 1, "alice sees only her other device")

		_, found := c.PeerByName("lb-bob-1")
		assert.False(t, found, "alice must not see bob")
	}

	// What all_peers_have_user_profiles checked across users: the peer's
	// owner is known.
	nm := a1.Netmap()
	_, hasProfile := nm.UserProfiles[nm.Peers[0].User()]
	assert.True(t, hasProfile, "peer's user profile is sent")

	nm = b1.Netmap()
	require.NotNil(t, nm)
	assert.Empty(t, nm.Peers, "bob must not see alice")
	assert.Len(t, nm.UserProfiles, 1, "bob gets only his own user profile")
}

// The removal path TestPeerRemovedAsDelta covered across users, within one.
func TestLinbeiPeerRemovedAsDeltaWithinUser(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t, servertest.WithMagicDNS("lb-delta.example.com"))
	user := srv.CreateUser(t, "lb-delta")

	c1 := servertest.NewClient(t, srv, "lb-delta-1",
		servertest.WithUser(user), servertest.WithDeltaUpdates())
	servertest.NewClient(t, srv, "lb-delta-2", servertest.WithUser(user))

	c1.WaitForPeers(t, 1, 15*time.Second)

	id := findNodeID(t, srv, "lb-delta-2")
	node, ok := srv.State().GetNodeByID(id)
	require.True(t, ok)

	changes, err := srv.State().DeleteNode(node)
	require.NoError(t, err)
	srv.App.Change(changes...)

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Contains(c, c1.DeltaRemovedPeers(), id.NodeID())
	}, 10*time.Second, 50*time.Millisecond, "peer removal never applied as a delta")
}
