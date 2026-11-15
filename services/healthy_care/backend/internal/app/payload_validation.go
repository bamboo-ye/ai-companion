package app

import (
	"fmt"
	"math"
)

func validatePayload(kind string, p map[string]any) error {
	if len(p) > 20 {
		return invalid("操作字段过多。")
	}
	arrayFields := map[string]bool{"ingredients": true, "allergies": true, "conditions": true, "current_medications": true}
	for key, value := range p {
		switch key {
		case "age", "score":
			if value == nil && key == "score" {
				continue
			}
			n, ok := value.(float64)
			if !ok || math.IsInf(n, 0) || math.IsNaN(n) || (key == "age" && n != math.Trunc(n)) {
				return invalid("数值字段不正确。")
			}
		case "granted":
			if _, ok := value.(bool); !ok {
				return invalid("授权必须明确为同意或撤回。")
			}
		case "symptoms":
			values, ok := value.([]any)
			if !ok || len(values) > 20 {
				return invalid("症状记录格式不正确。")
			}
			for _, v := range values {
				s, ok := v.(map[string]any)
				if !ok || !knownFields(s, "code", "assertion") || symptomLabels[text(s, "code")] == "" {
					return invalid("历史症状概念不在当前知识范围内。")
				}
				a := text(s, "assertion")
				if a != "present" && a != "absent" && a != "unknown" {
					return invalid("请明确症状的肯定、否定或未知状态。")
				}
			}
		default:
			if arrayFields[key] {
				values, ok := value.([]any)
				if !ok || len(values) > 30 {
					return invalid("列表字段不正确。")
				}
				for _, v := range values {
					if s, ok := v.(string); !ok || len(s) > 200 {
						return invalid("列表文字过长或格式不正确。")
					}
				}
			} else {
				if s, ok := value.(string); !ok || len(s) > 4000 {
					return invalid(fmt.Sprintf("字段%s应为有限长度的文字。", key))
				}
			}
		}
	}
	return nil
}
