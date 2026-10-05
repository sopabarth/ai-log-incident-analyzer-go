package llm

import (
	"fmt"
	"strings"

	"github.com/sopabarth/ai-log-incident-analyzer-go/internal/domain"
)

// The system prompts carry the wording of the Python service's prompts, so
// results from the two stay comparable in the eval. The instructions are fixed;
// everything specific to one incident goes in the user prompt.
var (
	classifySystemPrompt = `You are an SRE assistant that classifies application error logs.

Given a normalized error/stack trace plus its service context, respond with a
JSON object with EXACTLY these fields and nothing else:

- "category": one of ` + quotedList(domain.ErrorCategories()) + `
- "root_cause_summary": 1-2 plain-language sentences, under 300 characters.
- "confidence": float between 0.0 and 1.0, how confident you are in this
  classification given the evidence.
- "needs_human_review": boolean, true if the trace is ambiguous, truncated,
  contradictory, or doesn't clearly match a known pattern - in that case
  still give your best-guess category, but set this true and lower your
  confidence accordingly. Prefer true over guessing wildly.

Return ONLY the JSON object, no markdown fences, no commentary.
`

	prioritizeSystemPrompt = `You are an SRE assistant that sets the *priority* of an
already-classified application incident.

You are given the error category already determined for this incident, its
service/environment context, and the normalized error text. Respond with a
JSON object with EXACTLY these fields and nothing else:

- "priority": one of ` + quotedList(domain.Priorities()) + `
- "priority_reasoning": one short sentence, under 200 characters.

Judge priority using the blast radius actually described in the error text
(how many requests/users are affected, whether the failure is total or
partial, whether there's a working retry/fallback) together with the
service and environment - the same error category can be "critical" in
prod when it affects all requests on a checkout path, and "low" for a
single affected user or a background job with no user impact.

Return ONLY the JSON object, no markdown fences, no commentary.
`

	singleCallSystemPrompt = `You are an SRE assistant that triages application error logs.

Given a normalized error/stack trace plus its service context, respond with a
JSON object with EXACTLY these fields and nothing else:

- "category": one of ` + quotedList(domain.ErrorCategories()) + `
- "root_cause_summary": 1-2 plain-language sentences, under 300 characters.
- "priority": one of ` + quotedList(domain.Priorities()) + `
- "priority_reasoning": one short sentence, under 200 characters.
- "confidence": float between 0.0 and 1.0, how confident you are in this
  classification given the evidence.
- "needs_human_review": boolean, true if the trace is ambiguous, truncated,
  contradictory, or doesn't clearly match a known pattern - in that case
  still give your best-guess category/priority, but set this true and lower
  your confidence accordingly. Prefer true over guessing wildly.

Judge priority using the service, environment, and blast radius described in
the context - the same error type can be "critical" in prod on a checkout
path and "low" in a dev environment.

Return ONLY the JSON object, no markdown fences, no commentary.
`
)

// quotedList renders enum values as a JSON-style list: ["a", "b", "c"].
func quotedList[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", string(v))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// classifyUserPrompt is also the user prompt of the single-call mode: both
// just need the incident's context and error text.
func classifyUserPrompt(service string, env domain.Environment, normalizedText string) string {
	return fmt.Sprintf("service: %s\nenvironment: %s\nerror:\n%s", service, env, normalizedText)
}

// prioritizeUserPrompt adds the category that step 1 already decided.
func prioritizeUserPrompt(service string, env domain.Environment, normalizedText string, category domain.ErrorCategory) string {
	return fmt.Sprintf("category: %s\n%s", category, classifyUserPrompt(service, env, normalizedText))
}
