package state

import (
	"fmt"
	"net/netip"
	"slices"
	"testing"

	policyv2 "github.com/juanfont/headscale/hscontrol/policy/v2"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/stretchr/testify/require"
)

// Linbei fork: tests for linbei_isolation.go.

const policyAllowAll = `{
	"tagOwners": {"tag:srv": ["u1@"], "tag:web": ["u1@"]},
	"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]}`

// isolationTestNodes returns three nodes for each of two users, two tagged
// nodes and one user-owned node without a user, each with its own address.
func isolationTestNodes() types.Nodes {
	var nodes types.Nodes

	add := func(n types.Node) {
		ip := netip.AddrFrom4([4]byte{100, 64, 0, byte(n.ID)})
		n.IPv4 = &ip
		nodes = append(nodes, &n)
	}

	id := types.NodeID(1)
	for _, user := range []struct {
		id   uint
		name string
	}{{1, "u1"}, {2, "u2"}} {
		for i := range 3 {
			add(createTestNode(id, user.id, user.name, fmt.Sprintf("%s-%d", user.name, i)))
			id++
		}
	}

	for _, tag := range []string{"tag:srv", "tag:web"} {
		n := createTestNode(id, 1, "u1", "tagged-"+tag[4:])
		n.Tags = []string{tag}
		n.UserID, n.User = nil, nil
		add(n)
		id++
	}

	orphan := createTestNode(id, 1, "u1", "orphan")
	orphan.UserID, orphan.User = nil, nil
	add(orphan)

	return nodes
}

func sameGroup(a, b types.NodeView) bool {
	return isolationGroupOf(a) == isolationGroupOf(b)
}

// Under every policy shape — including one that allows everyone everything —
// the isolated adjacency is exactly upstream's adjacency restricted to pairs
// in the same ownership group. In particular no node ever sees another
// user's node.
func TestIsolatedPeerMapIsUpstreamWithinGroups(t *testing.T) {
	users := []types.User{{ID: 1, Name: "u1"}, {ID: 2, Name: "u2"}}

	for _, tc := range []struct {
		name string
		pol  string
	}{
		{name: "allow-all", pol: policyAllowAll},
		{name: "global", pol: policyGlobal},
		{name: "autogroup-self", pol: policyAutogroupSelf},
		{name: "via", pol: policyVia},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := isolationTestNodes()
			nodeViews := nodes.ViewSlice()

			pm, err := policyv2.NewPolicyManager([]byte(tc.pol), users, nodeViews)
			require.NoError(t, err)

			upstream := pm.BuildPeerMap(nodeViews)
			got := isolatedPeersFunc(pm)(nodeViews.AsSlice())

			byID := make(map[types.NodeID]types.NodeView, nodeViews.Len())
			for _, n := range nodeViews.All() {
				byID[n.ID()] = n
			}

			for _, n := range nodeViews.All() {
				var want []types.NodeID

				for _, peer := range upstream[n.ID()] {
					if sameGroup(n, byID[peer]) {
						want = append(want, peer)
					}
				}

				gotPeers := slices.Sorted(slices.Values(got[n.ID()]))
				require.Equal(t, slices.Sorted(slices.Values(want)), gotPeers,
					"node %d (%s)", n.ID(), n.Hostname())

				for _, peer := range gotPeers {
					require.True(t, sameGroup(n, byID[peer]),
						"node %d sees node %d across ownership groups", n.ID(), peer)
				}
			}
		})
	}
}

// The concrete promise, through the NodeStore wiring production uses: with a
// policy that allows everything, each user sees only their own devices,
// tagged nodes see only each other, and an ownerless node sees nobody.
func TestNodeStoreIsolatesUsersUnderAllowAll(t *testing.T) {
	users := []types.User{{ID: 1, Name: "u1"}, {ID: 2, Name: "u2"}}
	nodes := isolationTestNodes()

	pm, err := policyv2.NewPolicyManager([]byte(policyAllowAll), users, nodes.ViewSlice())
	require.NoError(t, err)

	store := NewNodeStore(nodes, isolatedPeersFunc(pm), TestBatchSize, TestBatchTimeout)
	store.Start()
	defer store.Stop()

	peerIDs := func(id types.NodeID) []types.NodeID {
		var ids []types.NodeID
		for _, p := range store.ListPeers(id).All() {
			ids = append(ids, p.ID())
		}

		return slices.Sorted(slices.Values(ids))
	}

	require.Equal(t, []types.NodeID{2, 3}, peerIDs(1), "u1 sees only u1")
	require.Equal(t, []types.NodeID{4, 5}, peerIDs(6), "u2 sees only u2")
	require.Equal(t, []types.NodeID{8}, peerIDs(7), "tagged sees only tagged")
	require.Empty(t, peerIDs(9), "ownerless node sees nobody")

	// Upstream's own PeersFunc would have connected everyone: this is what
	// the isolation removes.
	upstream := policyPeersFunc(pm)(nodes.ViewSlice().AsSlice())
	require.Len(t, upstream[1], len(nodes)-1, "sanity: upstream connects u1 to every other node")
}
