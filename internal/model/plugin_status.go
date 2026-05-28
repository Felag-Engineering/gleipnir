package model

// PluginStatus represents the review/lifecycle status of a plugin row.
// Values mirror the DB CHECK constraint in sql_schemas.sql.
type PluginStatus string

const (
	PluginStatusPendingReview PluginStatus = "pending_review"
	// PluginStatusActive marks a plugin that has passed admin review and is
	// eligible to run subprocesses.
	//
	// NOTE: internal/plugin/loader/install.go has an unexported duplicate
	// constant (statusActive = "active"). Consolidating it to use this type
	// is intentionally out of scope — loader is a lower layer that must not
	// import internal/model.
	PluginStatusActive  PluginStatus = "active"
	PluginStatusRemoved PluginStatus = "removed"
)
