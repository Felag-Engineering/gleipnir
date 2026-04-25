package migrations

// All returns the full ordered list of Go migrations. The order MUST match the
// call order in the original store.go Migrate() function — changing the order
// would skip or double-apply migrations on existing databases.
func All() []Migration {
	return []Migration{
		&AddThinkingStepType{},
		&AddTriggerQueue{},
		&AddWaitingForFeedback{},
		&AddFeedbackRequests{},
		&DropCapabilityRole{},
		&AddFeedbackExpiresAt{},
		&AddRunModel{},
		&AddUserPreferencesAndSessionUA{},
		&AddPollTriggerType{},
		&AddSystemAndModelSettings{},
		&AddOpenAICompatProviders{},
		&AddWebhookSecretEncrypted{},
		&DeleteUserPrefDefaultModel{},
		&AddRunsVersion{},
		&AddCronTriggerType{},
		&AddMCPToolEnabled{},
		&AddMCPAuthHeaders{},
	}
}
