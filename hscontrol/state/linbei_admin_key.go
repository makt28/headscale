package state

// Linbei fork: a fixed admin key chosen by the operator, accepted by the v1
// and v2 APIs exactly like an admin API key. sspapi keeps the same value in
// its own config, so a headscale rebuilt from scratch with the same key needs
// no change on the sspapi side (generated API keys live in the database and
// die with it). Loaded from linbei.admin_key_path, see
// hscontrol/linbei_admin_key.go.

import (
	"crypto/subtle"
	"sync/atomic"

	"github.com/juanfont/headscale/hscontrol/types"
)

var linbeiAdminKey atomic.Pointer[[]byte]

// SetLinbeiAdminKey installs the fixed admin key. An empty key turns it off.
func SetLinbeiAdminKey(key []byte) {
	if len(key) == 0 {
		linbeiAdminKey.Store(nil)

		return
	}

	k := append([]byte(nil), key...)
	linbeiAdminKey.Store(&k)
}

func linbeiAdminKeyMatches(token string) bool {
	k := linbeiAdminKey.Load()
	if k == nil || token == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(token), *k) == 1
}

// linbeiAdminAPIKey is what AuthenticateAPIKey returns for the fixed key: an
// admin key without an owning user, like a legacy CLI-created key.
func linbeiAdminAPIKey() *types.APIKey {
	return &types.APIKey{Prefix: "linbei-admin"}
}
