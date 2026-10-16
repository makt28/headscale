package state

import (
	"github.com/juanfont/headscale/hscontrol/policy"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"tailscale.com/types/views"
)

// Linbei fork: hard per-user isolation.
//
// The fork serves many unrelated customers from one control server. Upstream
// derives peer visibility from the policy alone, so a policy mistake or an
// upstream regression in autogroup:self would hand every customer the device
// names and home public IPs of every other customer — data that cannot be
// taken back once a client has it. Here isolation does not depend on the
// policy at all: peers are computed per ownership group, so nodes of different
// users never become peers whatever the policy says. Within a group the
// policy still applies exactly as upstream.
//
// Groups:
//   - every user-owned node is grouped with its user's other nodes;
//   - tagged nodes form one group of their own (operator infrastructure), and
//     are never peers of user-owned nodes;
//   - a user-owned node without a user stays alone (fail closed).
//
// Everything a client learns about other nodes (peer list, incremental
// changes, user profiles, MagicDNS records) is derived from this adjacency, so
// filtering here is the single choke point. Building per group also turns the
// upstream O(n²) pair scan over all nodes into O(Σ group²).
//
// This is the PeersFunc production uses; upstream tests keep calling
// [policyPeersFunc] directly and are unaffected.
func isolatedPeersFunc(pm policy.PolicyManager) PeersFunc {
	return func(nodes []types.NodeView) map[types.NodeID][]types.NodeID {
		// Same as policyPeersFunc: the policy manager must see every node,
		// tagged ones included, before any adjacency is built.
		_, err := pm.SetNodes(views.SliceOf(nodes))
		if err != nil {
			log.Error().Err(err).Msg("refreshing policy nodes before peer map build")
		}

		return buildIsolatedPeerMap(pm, nodes)
	}
}

func buildIsolatedPeerMap(
	pm policy.PolicyManager,
	nodes []types.NodeView,
) map[types.NodeID][]types.NodeID {
	groups := make(map[isolationGroup][]types.NodeView)

	var order []isolationGroup

	for _, node := range nodes {
		group := isolationGroupOf(node)
		if _, seen := groups[group]; !seen {
			order = append(order, group)
		}

		groups[group] = append(groups[group], node)
	}

	ret := make(map[types.NodeID][]types.NodeID, len(nodes))

	for _, group := range order {
		members := groups[group]
		if len(members) < 2 {
			continue
		}

		for id, peers := range pm.BuildPeerMap(views.SliceOf(members)) {
			ret[id] = peers
		}
	}

	return ret
}

type isolationGroup struct {
	tagged bool
	user   types.UserID
	// set only for an ownerless user-owned node, which is isolated alone
	orphan types.NodeID
}

func isolationGroupOf(node types.NodeView) isolationGroup {
	if node.IsTagged() {
		return isolationGroup{tagged: true}
	}

	if uid := node.UserID(); uid.Valid() {
		return isolationGroup{user: types.UserID(uid.Get())}
	}

	return isolationGroup{orphan: node.ID()}
}
