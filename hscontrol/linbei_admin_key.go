package hscontrol

// Linbei fork: loads the fixed admin key (see state/linbei_admin_key.go).
//
//   linbei:
//     admin_key_path: /etc/headscale/admin_key   # at least 32 characters
//
// Not configured = off. Configured but unreadable or too short = refuse to
// start: sspapi would otherwise fail every request without anyone noticing.

import (
	"bytes"
	"fmt"
	"os"

	"github.com/juanfont/headscale/hscontrol/state"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

const linbeiAdminKeyMinLen = 32

func (h *Headscale) loadLinbeiAdminKey() {
	key, err := linbeiAdminKeyFromConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("linbei admin key")
	}

	if key == nil {
		return
	}

	// Only ever set, never cleared: tests build many servers in one process.
	state.SetLinbeiAdminKey(key)
	log.Info().Msg("linbei fixed admin key enabled")
}

func linbeiAdminKeyFromConfig() ([]byte, error) {
	path := viper.GetString("linbei.admin_key_path")
	if path == "" {
		return nil, nil //nolint:nilnil // not configured is not an error
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading linbei.admin_key_path: %w", err)
	}

	key := bytes.TrimSpace(content)
	if len(key) < linbeiAdminKeyMinLen {
		return nil, fmt.Errorf("linbei.admin_key_path: key must be at least %d characters", linbeiAdminKeyMinLen)
	}

	return key, nil
}
