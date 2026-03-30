package policy

import (
	"fmt"
	"strings"
	"time"

	"github.com/rapp992/gleipnir/internal/config"
	"github.com/rapp992/gleipnir/internal/model"
)

// defaultPreamble is used when the policy does not supply its own preamble.
const defaultPreamble = `You are a BoundAgent — an autonomous agent operating within explicitly defined boundaries.

You have two categories of capabilities:

- Tools: your available tools for interacting with systems.
- Feedback: a channel to consult a human operator. Calling a feedback tool will pause this run until an operator responds. Use this when you are uncertain about intended scope, when observations reveal something unexpected, or when proceeding would require an assumption you cannot verify.

Your operating principles:
1. Act deliberately. Gather information before making changes. Do not take additional actions because they seem useful.
2. Ask when uncertain. A paused run that asks a good question is better than a completed run that made a wrong assumption.
3. Be transparent. Your reasoning is fully audited. Explain what you observed, what you concluded, and why you acted.`

// RenderSystemPrompt produces the full system prompt for an agent run.
// It combines the preamble (policy-supplied or default), the generated
// capabilities block listing granted tools, and the task instructions.
// The capabilities block is generated at run start and never persisted (ADR-012).
func RenderSystemPrompt(p *model.ParsedPolicy, granted []model.GrantedTool, now time.Time) string {
	var b strings.Builder

	preamble := p.Agent.Preamble
	if preamble == "" {
		preamble = defaultPreamble
	}
	b.WriteString(preamble)
	b.WriteString("\n\nThis run started at: " + now.Format(config.TimestampFormat))
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
	var tools, feedback []model.GrantedTool
	for _, g := range granted {
		switch g.Role {
		case model.CapabilityRoleTool:
			tools = append(tools, g)
		case model.CapabilityRoleFeedback:
			feedback = append(feedback, g)
		}
	}

	var b strings.Builder
	b.WriteString("## Capabilities\n\n")

	b.WriteString("### Tools\n")
	if len(tools) == 0 {
		b.WriteString("None.\n")
	} else {
		for _, t := range tools {
			fmt.Fprintf(&b, "- %s.%s\n", t.ServerName, t.ToolName)
		}
	}

	b.WriteString("\n### Feedback (human-in-the-loop)\n")
	if len(feedback) == 0 {
		b.WriteString("Use the built-in feedback channel to consult a human operator.\n")
	} else {
		for _, f := range feedback {
			fmt.Fprintf(&b, "- %s.%s\n", f.ServerName, f.ToolName)
		}
	}

	return b.String()
}
