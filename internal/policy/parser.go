package policy

import (
	"fmt"

	"github.com/rapp992/gleipnir/internal/model"
	"gopkg.in/yaml.v3"
)

// Parse parses a raw YAML policy blob into a ParsedPolicy.
// It does not validate the result — call Validate separately.
func Parse(raw string) (*model.ParsedPolicy, error) {
	var r rawPolicy
	if err := yaml.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("parse policy yaml: %w", err)
	}
	// TODO: convert rawPolicy → model.ParsedPolicy
	panic("not implemented")
}

// rawPolicy is the intermediate YAML representation used during parsing.
// Field names match the policy schema documented in schemas/policy.yaml.
type rawPolicy struct {
	Name         string          `yaml:"name"`
	Description  string          `yaml:"description"`
	Trigger      rawTrigger      `yaml:"trigger"`
	Capabilities rawCapabilities `yaml:"capabilities"`
	Agent        rawAgent        `yaml:"agent"`
}

type rawTrigger struct {
	Type     string      `yaml:"type"`
	Schedule string      `yaml:"schedule"` // cron only
	Interval string      `yaml:"interval"` // poll only
	Request  *rawRequest `yaml:"request"`  // poll only
	Filter   string      `yaml:"filter"`   // poll only
}

type rawRequest struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"`
}

type rawCapabilities struct {
	Sensors   []rawSensor   `yaml:"sensors"`
	Actuators []rawActuator `yaml:"actuators"`
	Feedback  []string      `yaml:"feedback"`
}

type rawSensor struct {
	Tool string `yaml:"tool"`
}

type rawActuator struct {
	Tool      string `yaml:"tool"`
	Approval  string `yaml:"approval"`
	Timeout   string `yaml:"timeout"`
	OnTimeout string `yaml:"on_timeout"`
}

type rawAgent struct {
	Preamble    string    `yaml:"preamble"`
	Task        string    `yaml:"task"`
	Limits      rawLimits `yaml:"limits"`
	Concurrency string    `yaml:"concurrency"`
}

type rawLimits struct {
	MaxTokensPerRun    int `yaml:"max_tokens_per_run"`
	MaxToolCallsPerRun int `yaml:"max_tool_calls_per_run"`
}
