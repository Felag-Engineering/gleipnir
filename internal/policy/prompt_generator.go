package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/infra/config"
	"github.com/rapp992/gleipnir/internal/model"
)

// defaultPreambleBase is the portion of the default preamble that applies to
// all agents, regardless of whether feedback is enabled.
const defaultPreambleBase = `You are a BoundAgent — an autonomous agent operating within explicitly defined boundaries.

You have two categories of capabilities:

- Tools: your available tools for interacting with systems.`

// feedbackPreambleAddendum is appended to the default preamble only when the
// policy has feedback enabled. Omitting it when feedback is disabled avoids
// misleading the agent about capabilities it does not have.
const feedbackPreambleAddendum = `
- Feedback: a channel to consult a human operator. Calling gleipnir.ask_operator will pause this run until an operator responds. Use this when you are uncertain about intended scope, when observations reveal something unexpected, or when proceeding would require an assumption you cannot verify.`

// defaultPreambleSuffix is the operating principles block, always appended after
// the capability list in the default preamble.
const defaultPreambleSuffix = `

Your operating principles:
1. Act deliberately. Gather information before making changes. Do not take additional actions because they seem useful.
2. Ask when uncertain. A paused run that asks a good question is better than a completed run that made a wrong assumption.
3. Be transparent. Your reasoning is fully audited. Explain what you observed, what you concluded, and why you acted.`

// RenderSystemPrompt produces the full system prompt for an agent run.
// It combines the preamble (policy-supplied or default), the generated
// capabilities block listing granted tools, and the task instructions.
// The capabilities block is generated at run start and never persisted (ADR-012).
// When a policy uses the default preamble and feedback is disabled, the feedback
// paragraph is omitted from the preamble to avoid misleading the agent.
func RenderSystemPrompt(p *model.ParsedPolicy, granted []model.GrantedTool, now time.Time) string {
	var b strings.Builder

	preamble := p.Agent.Preamble
	if preamble == "" {
		// Build the default preamble conditionally based on feedback config.
		// Custom preambles are left as-is — the operator controls their content.
		preamble = defaultPreambleBase
		if p.Capabilities.Feedback.Enabled {
			preamble += feedbackPreambleAddendum
		}
		preamble += defaultPreambleSuffix
	}
	b.WriteString(preamble)
	b.WriteString("\n\nThis run started at: " + now.Format(config.TimestampFormat))
	b.WriteString("\n\n")

	b.WriteString(renderCapabilitiesBlock(granted, p.Capabilities.Feedback))
	b.WriteString("\n\n")

	b.WriteString("## Your task\n\n")
	b.WriteString(p.Agent.Task)

	return b.String()
}

// renderCapabilitiesBlock produces the "## Capabilities" section of the
// system prompt listing all granted tools and, when feedback is enabled,
// the built-in feedback channel.
func renderCapabilitiesBlock(granted []model.GrantedTool, feedback model.FeedbackConfig) string {
	var b strings.Builder
	b.WriteString("## Capabilities\n\n")

	b.WriteString("### Tools\n")
	if len(granted) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, t := range granted {
			fmt.Fprintf(&b, "- %s.%s\n", t.ServerName, t.ToolName)
		}
	}

	if feedback.Enabled {
		b.WriteString("\n### Feedback (human-in-the-loop)\n")
		b.WriteString("Call `gleipnir.ask_operator` to consult a human operator. ")
		b.WriteString("The run will pause until the operator responds.\n")
	}

	return b.String()
}
