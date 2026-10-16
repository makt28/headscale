package policy

import (
	"net/netip"

	"tailscale.com/net/tsaddr"
)

// Linbei fork: a node's own exit-node advertisement approves itself.
//
// Upstream approves routes only through the policy's autoApprovers or an
// admin, because in a shared tailnet a node claiming to be an exit node can
// pull other people's traffic through itself. Here that cannot happen: users
// are hard-isolated (see state.isolatedPeersFunc), so an exit node is only
// visible to its owner's other devices, and those still have to pick it
// explicitly. The owner advertising it is the consent. The policy cannot
// express this either — autoApprovers only take fixed usernames, groups and
// tags, while customers' users are created on demand.
//
// Only the two exit routes are approved. Subnet routes keep upstream
// behaviour: a device advertising 10.0.0.0/8 still needs the policy or an
// admin, so a misconfigured device cannot quietly start capturing a LAN range
// on its owner's other devices.
func linbeiAutoApproveExitRoute(route netip.Prefix) bool {
	return tsaddr.IsExitRoute(route)
}
