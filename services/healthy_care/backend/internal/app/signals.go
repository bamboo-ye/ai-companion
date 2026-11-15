package app

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
)

var symptomLabels = map[string]string{"thirst": "持续口渴", "urination": "排尿增多", "weight_loss": "非主动体重下降"}

func matchHistory(observations []map[string]any, now time.Time) []map[string]any {
	latest := map[string]map[string]any{}
	for _, o := range observations {
		date, ok := o["observed_date"].(time.Time)
		if !ok {
			continue
		}
		if date.After(now) || date.Before(now.AddDate(0, 0, -30)) {
			continue
		}
		data, _ := o["data"].(map[string]any)
		symptoms, _ := data["symptoms"].([]any)
		for _, v := range symptoms {
			s, ok := v.(map[string]any)
			if !ok {
				continue
			}
			code := text(s, "code")
			if symptomLabels[code] == "" {
				continue
			}
			previous := latest[code]
			if previous == nil || date.After(previous["at"].(time.Time)) || date.Equal(previous["at"].(time.Time)) {
				latest[code] = map[string]any{"id": fmt.Sprint(o["id"]), "label": symptomLabels[code], "assertion": text(s, "assertion"), "date": date.Format("2006-01-02"), "at": date}
			}
		}
	}
	evidence := []map[string]any{}
	for _, code := range []string{"thirst", "urination", "weight_loss"} {
		v := latest[code]
		if v == nil || v["assertion"] != "present" {
			return []map[string]any{}
		}
		delete(v, "at")
		evidence = append(evidence, v)
	}
	return []map[string]any{{"id": "history-thirst-v1", "title": "这些记录值得一起进一步核实", "message": "近30天记录中同时出现了口渴、排尿及体重变化。请核对是否仍持续，并向医护人员说明这些实际变化，必要时检查。本提示是有限知识匹配，不是疾病诊断或疾病阶段判断。", "source_url": "https://www.nhs.uk/conditions/type-2-diabetes/symptoms/", "knowledge_version": "reference-2026-10-19", "rule_version": "history-structured-30d-1", "evidence": evidence}}
}
func (a *App) historySignals(c *gin.Context) {
	items, err := rows(c, a.DB, "SELECT id,observed_date,data FROM observations WHERE user_id=$1 ORDER BY observed_date,recorded_at", actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": matchHistory(items, time.Now())})
}
