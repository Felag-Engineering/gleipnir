// Matches policy_handler.go → runSummary
export interface ApiRunSummary {
  id: string
  status: string
  started_at: string
  token_cost: number
}

// Matches policy_handler.go → policyListItem
export interface ApiPolicyListItem {
  id: string
  name: string
  trigger_type: string
  folder: string
  created_at: string
  updated_at: string
  latest_run: ApiRunSummary | null
}

// Matches trigger/runs_handler.go → RunSummary struct
export interface ApiRun {
  id: string
  policy_id: string
  status: string
  trigger_type: string
  started_at: string
  completed_at: string | null
  token_cost: number
  error: string | null
  created_at: string
}
