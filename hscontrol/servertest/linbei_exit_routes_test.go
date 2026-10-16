package servertest_test

import (
	"context"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/servertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/tailcfg"
)

// Linbei fork: a device's exit-node advertisement approves itself
// (hscontrol/policy/linbei_exit_routes.go), with no policy and no admin.
// Its owner's other devices can then use it; other users still cannot see it.
func TestLinbeiExitNodeSelfApproves(t *testing.T) {
	t.Parallel()

	srv := servertest.NewServer(t)
	alice := srv.CreateUser(t, "lb-exit-alice")
	bob := srv.CreateUser(t, "lb-exit-bob")

	exitDev := servertest.NewClient(t, srv, "lb-exit-alice-exit", servertest.WithUser(alice))
	laptop := servertest.NewClient(t, srv, "lb-exit-alice-laptop", servertest.WithUser(alice))
	other := servertest.NewClient(t, srv, "lb-exit-bob-1", servertest.WithUser(bob))

	exitDev.WaitForPeers(t, 1, 15*time.Second)
	laptop.WaitForPeers(t, 1, 15*time.Second)

	exit4 := netip.MustParsePrefix("0.0.0.0/0")
	exit6 := netip.MustParsePrefix("::/0")
	subnet := netip.MustParsePrefix("10.77.0.0/24")

	exitDev.Direct().SetHostinfo(&tailcfg.Hostinfo{
		BackendLogID: "servertest-" + exitDev.Name,
		Hostname:     exitDev.Name,
		RoutableIPs:  []netip.Prefix{exit4, exit6, subnet},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = exitDev.Direct().SendUpdate(ctx)

	nodeID := findNodeID(t, srv, exitDev.Name)

	require.Eventually(t, func() bool {
		nv, ok := srv.State().GetNodeByID(nodeID)
		if !ok {
			return false
		}

		approved := nv.ApprovedRoutes().AsSlice()

		return slices.Contains(approved, exit4) && slices.Contains(approved, exit6)
	}, 10*time.Second, 50*time.Millisecond, "exit routes are approved without a policy or an admin")

	nv, ok := srv.State().GetNodeByID(nodeID)
	require.True(t, ok)
	assert.NotContains(t, nv.ApprovedRoutes().AsSlice(), subnet, "subnet routes still need an admin")

	// The owner's other device sees it as an exit node.
	require.Eventually(t, func() bool {
		peer, found := laptop.PeerByName(exitDev.Name)
		if !found {
			return false
		}

		allowed := peer.AllowedIPs().AsSlice()

		return slices.Contains(allowed, exit4) && slices.Contains(allowed, exit6)
	}, 10*time.Second, 50*time.Millisecond, "alice's laptop can use the exit node")

	peer, _ := laptop.PeerByName(exitDev.Name)
	assert.NotContains(t, peer.AllowedIPs().AsSlice(), subnet)

	// Give any cross-user update time to arrive before asserting its absence.
	time.Sleep(2 * time.Second)

	_, found := other.PeerByName(exitDev.Name)
	assert.False(t, found, "bob must not see alice's exit node")

	nm := other.Netmap()
	require.NotNil(t, nm)
	assert.Empty(t, nm.Peers)
}
