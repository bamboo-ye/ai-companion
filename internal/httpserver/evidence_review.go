package httpserver

// Only content-free, bounded fields belong in the public review summary.
// Never echo arbitrary worker output through this supposedly safe projection.
func publicEvidenceReview(value any) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result := map[string]any{}
	for key, maximum := range map[string]float64{"total_issues": 12, "resolved_issues": 12, "rounds": 2, "additional_reads": 2, "model_calls": 6} {
		number, valid := raw[key].(float64)
		if key == "model_calls" && raw[key] == nil {
			number, valid = 0, true // Older summaries omitted this optional counter.
		}
		if !valid || number < 0 || number > maximum || number != float64(int(number)) {
			return nil
		}
		result[key] = int(number)
	}
	total, resolved := result["total_issues"].(int), result["resolved_issues"].(int)
	if total == 0 || resolved > total {
		return nil
	}
	isolated, isolationOK := raw["isolation_passed"].(bool)
	verified, verifiedOK := raw["sources_verified"].(bool)
	if !isolationOK || !verifiedOK {
		return nil
	}
	reason, _ := raw["termination_reason"].(string)
	switch reason {
	case "source_version_changed", "snapshot_changed", "evidence_access_changed":
		reason, verified = "evidence_access_changed", false
	case "resolved", "no_new_evidence", "evidence_unavailable", "deadline_exceeded", "round_limit", "independence_failed", "debate_model_budget", "evidence_read_failed", "input_budget_exceeded", "debate_unavailable", "model_access_unavailable", "repeated_state", "tool_resume_limit", "action_budget_exhausted", "debate_budget_overrun":
	default:
		reason = "verification_incomplete"
	}
	if reason == "independence_failed" {
		isolated = false
	}
	if !isolated || !verified {
		resolved = 0
	}
	status := "unresolved"
	if resolved == total {
		status = "resolved"
	} else if resolved > 0 {
		status = "partially_resolved"
	}
	if reason == "resolved" && status != "resolved" {
		reason = "verification_incomplete"
	}
	result["status"], result["resolved_issues"] = status, resolved
	result["isolation_passed"], result["sources_verified"], result["termination_reason"] = isolated, verified, reason
	return result
}

func sourceInvalidation(output map[string]any) bool {
	if output["outcome"] == "evidence_access_changed" {
		return true
	}
	review, _ := output["evidence_review"].(map[string]any)
	switch review["termination_reason"] {
	case "evidence_access_changed", "source_version_changed", "snapshot_changed":
		return true
	}
	return false
}
