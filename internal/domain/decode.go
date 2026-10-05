package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decodeRequired unmarshals a JSON object into dst after checking that every
// key in required is present and not null. encoding/json alone cannot tell a
// missing key from a zero value, and an LLM that omits "needs_human_review"
// or "confidence" must not be read as "false" or "0".
func decodeRequired(data []byte, dst any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var missing []string
	for _, key := range required {
		if raw, ok := fields[key]; !ok || string(raw) == "null" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing or null field(s): %s", strings.Join(missing, ", "))
	}
	return json.Unmarshal(data, dst)
}
