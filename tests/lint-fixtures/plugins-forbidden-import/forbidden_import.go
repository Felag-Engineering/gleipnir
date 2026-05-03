//go:build never

// Fixture for scripts/lint-plugins-self-test.sh — never compiled.
// Intentionally imports a host internal package; the lint rule must reject this.
package fixture

import (
	"github.com/felag-engineering/gleipnir/internal/db"
)
