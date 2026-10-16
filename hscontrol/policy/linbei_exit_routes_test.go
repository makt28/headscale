package policy

import (
	"net/netip"
	"testing"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestLinbeiExitRoutesSelfApprove(t *testing.T) {
	user := types.User{ID: 1, Name: "lb-1"}
	node := &types.Node{
		ID:             1,
		MachineKey:     key.NewMachine().Public(),
		NodeKey:        key.NewNode().Public(),
		Hostname:       "exit",
		UserID:         new(user.ID),
		User:           new(user),
		RegisterMethod: util.RegisterMethodAuthKey,
		IPv4:           new(netip.MustParseAddr("100.64.0.1")),
	}

	exit4 := netip.MustParsePrefix("0.0.0.0/0")
	exit6 := netip.MustParsePrefix("::/0")
	subnet := netip.MustParsePrefix("10.0.0.0/8")
	approvedSubnet := netip.MustParsePrefix("192.168.1.0/24")

	// No policy (what production runs) and a policy that approves nothing:
	// neither may approve the subnet, both must approve the exit routes.
	policies := map[string][]byte{
		"no_policy": nil,
		"policy_without_auto_approvers": []byte(`{
			"acls": [{"action": "accept", "src": ["*"], "dst": ["*:*"]}]
		}`),
	}

	for name, pol := range policies {
		t.Run(name, func(t *testing.T) {
			for _, pmf := range PolicyManagerFuncsForTest(pol) {
				pm, err := pmf([]types.User{user}, views.SliceOf([]types.NodeView{node.View()}))
				require.NoError(t, err)

				approved, changed := ApproveRoutesWithPolicy(pm, node.View(), nil,
					[]netip.Prefix{exit4, exit6, subnet})
				assert.True(t, changed)
				assert.Equal(t, []netip.Prefix{exit4, exit6}, approved,
					"exit routes approve themselves, the subnet waits for an admin")

				// Already-approved routes are kept, as upstream.
				approved, changed = ApproveRoutesWithPolicy(pm, node.View(),
					[]netip.Prefix{approvedSubnet}, []netip.Prefix{exit6, subnet})
				assert.True(t, changed)
				assert.Equal(t, []netip.Prefix{approvedSubnet, exit6}, approved, "sorted: IPv4 before IPv6")

				// Nothing new to approve: no change reported.
				_, changed = ApproveRoutesWithPolicy(pm, node.View(),
					[]netip.Prefix{exit4, exit6}, []netip.Prefix{exit4, exit6, subnet})
				assert.False(t, changed)
			}
		})
	}
}

func TestLinbeiAutoApproveExitRouteOnlyExactExitRoutes(t *testing.T) {
	for _, s := range []string{"0.0.0.0/0", "::/0"} {
		assert.True(t, linbeiAutoApproveExitRoute(netip.MustParsePrefix(s)), s)
	}

	for _, s := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "10.0.0.0/8", "100.64.0.0/10"} {
		assert.False(t, linbeiAutoApproveExitRoute(netip.MustParsePrefix(s)), s)
	}
}
