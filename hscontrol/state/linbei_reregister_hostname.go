package state

import (
	"github.com/juanfont/headscale/hscontrol/types"
	"tailscale.com/util/dnsname"
)

// Linbei fork: a re-registration carrying a new hostname re-derives GivenName
// the same way a MapRequest does (UpdateNodeFromMapRequest).
//
// Upstream only updates Hostname here (see the TODO in reregisterNodeWithPAK
// about using the MapRequest merge semantics). A client that restarts with an
// auth key re-registers before its first MapRequest, so the new hostname lands
// in Hostname first and the MapRequest then sees no hostname change: the
// DNS name stays on the old hostname forever. Our desktop client lets users
// rename the device and restarts its kernel with an auth key, which hit this
// on 2026-10-17 (Hostname "nec", Name still "desktop-gk2q71f").
//
// Same rule as the MapRequest path: only re-derive while GivenName is still
// what the old hostname would produce (possibly with a "-N" collision bump),
// so an admin rename is preserved. The NodeStore update bumps the new name on
// collision.
func linbeiRederiveGivenName(node *types.Node, hostname string) {
	if hostname == "" || hostname == node.Hostname {
		return
	}
	if isAutoDerivedGivenName(node.GivenName, node.Hostname) {
		node.GivenName = dnsname.SanitizeHostname(hostname)
	}
}
