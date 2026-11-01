package model

import (
	"encoding/json"
)

// ReviewSchema supplies the provider-side structured contract. Host validation
// remains authoritative, including evidence references and changed-line anchors.
func ReviewSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["schema_version","summary","findings","unresolved_questions","model_merge_risk","merge_risk_rationale","merge_risk_evidence_ids","limitations"],"properties":{"schema_version":{"type":"integer","enum":[1]},"summary":{"type":"string"},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["severity","category","title","trigger","consequence","path","line","evidence_ids"],"properties":{"severity":{"type":"string","enum":["critical","high","medium","low","info"]},"category":{"type":"string"},"title":{"type":"string"},"trigger":{"type":"string"},"consequence":{"type":"string"},"path":{"type":"string"},"line":{"type":"integer","minimum":0},"side":{"type":"string","enum":["old","new"]},"evidence_ids":{"type":"array","items":{"type":"string"}}}}},"unresolved_questions":{"type":"array","items":{"type":"string"}},"model_merge_risk":{"type":"integer","minimum":1,"maximum":10},"merge_risk_rationale":{"type":"string"},"merge_risk_evidence_ids":{"type":"array","items":{"type":"string"}},"limitations":{"type":"array","items":{"type":"string"}}}}`)
}

// ReleaseSchema is separate from merge-risk scoring and never authorizes release.
func ReleaseSchema() json.RawMessage {
	props := map[string]any{"schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "model_estimated_risk": map[string]any{"type": "string", "enum": []string{"low", "moderate", "high", "critical"}}, "risk_rationale": map[string]any{"type": "string"}}
	required := []string{"schema_version", "model_estimated_risk", "risk_rationale"}
	for _, k := range []string{"risk_items", "cross_change_interactions", "notable_changes", "required_actions", "migration_and_rollout_notes", "rollback_and_recovery_notes", "smoke_test_and_monitoring_suggestions", "unresolved_questions", "evidence_ids", "limitations"} {
		required = append(required, k)
		item := map[string]any{"type": "string"}
		if k != "evidence_ids" {
			item = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"text", "classification", "evidence_ids"}, "properties": map[string]any{"text": map[string]any{"type": "string"}, "classification": map[string]any{"type": "string", "enum": []string{"factual", "suggested", "unknown"}}, "evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "source_anchor": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"evidence_id", "path", "line"}, "properties": map[string]any{"evidence_id": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}, "line": map[string]any{"type": "integer", "minimum": 1}}}}}
		}
		props[k] = map[string]any{"type": "array", "items": item}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required})
	return b
}
