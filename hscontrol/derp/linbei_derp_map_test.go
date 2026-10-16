package derp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Linbei fork: the shipped client DERP map must parse with the keys we wrote
// (a typo decodes to nothing and the field silently keeps its zero value).
func TestLinbeiDERPMapParses(t *testing.T) {
	derpMap, err := loadDERPMapFromPath("../../linbei/derp-map.yaml")
	require.NoError(t, err)
	require.Len(t, derpMap.Regions, 1)

	region := derpMap.Regions[900]
	require.NotNil(t, region)
	require.Equal(t, "linbei", region.RegionCode)

	relay := region.Nodes[0]
	require.Equal(t, "derp.linbei.internal", relay.HostName)
	require.True(t, relay.InsecureForTests, "relays use self-signed certificates")
	require.False(t, relay.STUNOnly)
	require.Equal(t, 443, relay.DERPPort)
	require.Equal(t, -1, relay.STUNPort, "the virtual domain cannot be reached over STUN")

	for _, node := range region.Nodes[1:] {
		require.True(t, node.STUNOnly, node.Name)
		require.Equal(t, 3478, node.STUNPort, node.Name)
		require.Equal(t, 900, int(node.RegionID), node.Name)
	}
}
