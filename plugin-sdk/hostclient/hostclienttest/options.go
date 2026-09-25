package hostclienttest

// Option configures a Server at construction time (New or NewServer).
type Option func(*Server)

// WithToken overrides the bearer token the fake requires. Defaults to a
// fixed fake token; most tests do not need this — construct the client with
// hostclient.New() with no options (picked up via NewServer's environment
// wiring), or use Server.Token() to read whatever value is in effect.
func WithToken(token string) Option {
	return func(s *Server) { s.token = token }
}

// WithInstanceConfigJSON sets the JSON served by host/get_instance_config.
// Defaults to "{}".
func WithInstanceConfigJSON(configJSON string) Option {
	return func(s *Server) { s.instanceConfigJSON = configJSON }
}

// WithCredentialsJSON sets the JSON served by host/get_credentials. Defaults
// to "" — no credentials configured, a valid state rather than an error.
func WithCredentialsJSON(credentialsJSON string) Option {
	return func(s *Server) { s.credentialsJSON = credentialsJSON }
}

// WithRunContext associates callID with the run context host/get_run_context
// returns when a request carries that Gleipnir-Call-Id header. A call id
// with no configured entry gets a failed_precondition HostError, matching a
// real call id that is not currently in flight.
func WithRunContext(callID string, rc RunContext) Option {
	return func(s *Server) { s.runContexts[callID] = rc }
}

// WithRunHistory seeds the canned data host/run_history_read serves. Without
// this option it returns an empty list.
func WithRunHistory(runs []RunSummary) Option {
	return func(s *Server) { s.runHistory = runs }
}

// WithUserDirectory seeds the canned data host/user_directory_read serves.
// Without this option it returns an empty list.
func WithUserDirectory(users []UserEntry) Option {
	return func(s *Server) { s.userDirectory = users }
}

// WithAuthorizedActor adds actorExternalID to the default host/authorize_actor
// policy's allow set, resolving to userID when authorized. Ignored once
// WithAuthorizePolicy is also given — the policy function takes over
// entirely rather than layering on top of the set.
func WithAuthorizedActor(actorExternalID, userID string) Option {
	return func(s *Server) { s.authorizedActors[actorExternalID] = userID }
}

// WithAuthorizePolicy replaces the default set-membership check with a
// caller-supplied decision function for host/authorize_actor.
func WithAuthorizePolicy(policy ActorAuthorizer) Option {
	return func(s *Server) { s.authorizePolicy = policy }
}

// WithIdentityBinder configures how host/submit_identity_proof resolves a
// proof. Without this option every proof is rejected with
// ReasonNoPendingLink, modeling a host with no identity-link flow
// configured.
func WithIdentityBinder(binder IdentityBinder) Option {
	return func(s *Server) { s.binder = binder }
}

// WithUserConfig sets the JSON host/get_user_config serves for one external
// user id. A user with no configured entry gets "{}", never an error.
func WithUserConfig(externalUserID, userConfigJSON string) Option {
	return func(s *Server) { s.userConfigs[externalUserID] = userConfigJSON }
}
