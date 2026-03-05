package policy

import (
	"strings"

	"github.com/rapp992/gleipnir/internal/model"
)

// defaultPreamble is used when the policy does not supply its own preamble.
const defaultPreamble = `You are a BoundAgent — an autonomous agent operating within explicitly defined boundaries.

You have three categories of tools available to you:

- Sensors: read-only tools for observing the world. Use these freely and thoroughly before acting.
- Actuators: tools that affect the world. Use these deliberately — only after you have observed enough to be confident, and only when the task requires it.
- Feedback: a channel to consult a human operator. Use this when you are uncertain about intended scope, when observations reveal something unexpected, or when proceeding would require an assumption you cannot verify.

Your operating principles:
1. Observe before acting. Use your sensors to build a complete picture before calling any actuator.
2. Act minimally. Do what the task requires. Do not take additional actions because they seem useful.
3. Ask when uncertain. A paused run that asks a good question is better than a completed run that made a wrong assumption.
4. Be transparent. Your reasoning is fully audited. Explain what you observed, what you concluded, and why you acted.`

// RenderSystemPrompt produces the full system prompt for an agent run.
// It combines the preamble (policy-supplied or default), the generated
// capabilities block listing granted tools, and the task instructions.
// The capabilities block is generated at run start and never persisted (ADR-012).
func RenderSystemPrompt(p *model.ParsedPolicy, granted []model.GrantedTool) string {
	var b strings.Builder

	preamble := p.Agent.Preamble
	if preamble == "" {
		preamble = defaultPreamble
	}
	b.WriteString(preamble)
	b.WriteString("\n\n")

	b.WriteString(renderCapabilitiesBlock(granted))
	b.WriteString("\n\n")

	b.WriteString("## Your task\n\n")
	b.WriteString(p.Agent.Task)

	return b.String()
}

// renderCapabilitiesBlock produces the "## Capabilities" section of the
// system prompt listing each granted tool by role.
func renderCapabilitiesBlock(granted []model.GrantedTool) string {
	// TODO: group tools by role, format sensor/actuator/feedback subsections,
	// annotate approval-required actuators so the agent is aware of the gate.
	panic("not implemented")
}
