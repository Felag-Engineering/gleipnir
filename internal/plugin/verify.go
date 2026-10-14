package plugin

import "github.com/felag-engineering/gleipnir/internal/plugin/loader"

// Verifier is the Minisign bundle verifier. It lives in internal/plugin/loader
// so the container-substrate build can verify bundles without linking this
// package's go-plugin subprocess runtime; the alias keeps v1 callers unchanged.
type Verifier = loader.Verifier
