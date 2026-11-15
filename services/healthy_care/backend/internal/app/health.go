package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (a *App) state(ctx context.Context, user string) (map[string]any, error) {
	out := map[string]any{}
	queries := map[string]string{
		"profile":         "SELECT data,version FROM profiles WHERE user_id=$1",
		"episodes":        "SELECT id,topic,complaint,started_at,onset_known,status,version,archive_reason FROM episodes WHERE user_id=$1 ORDER BY started_at DESC",
		"observations":    "SELECT id,episode_id,observed_date,data,version,recorded_at,source_message_id FROM observations WHERE user_id=$1 ORDER BY observed_date,id",
		"medications":     "SELECT id,episode_id,data,version,recorded_at FROM medication_uses WHERE user_id=$1 ORDER BY recorded_at DESC",
		"tasks":           "SELECT id,episode_id,title,due_at,recurrence,status,version FROM care_tasks WHERE user_id=$1 ORDER BY due_at",
		"pending_actions": "SELECT id,kind,payload,expected_version,status,expires_at FROM actions WHERE user_id=$1 AND status='pending' AND expires_at>now() ORDER BY created_at",
	}
	for key, sql := range queries {
		v, err := rows(ctx, a.DB, sql, user)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	out["medication_checks"] = medicationChecks(out["medications"].([]map[string]any))
	leaflets, err := loadLeaflets(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	profile := map[string]any{}
	if values := out["profile"].([]map[string]any); len(values) > 0 {
		profile, _ = values[0]["data"].(map[string]any)
	}
	out["product_information"] = productInformation(leaflets, out["medications"].([]map[string]any), profile, time.Now(), a.Config.AllowSyntheticKnowledge)
	out["medication_checks"] = append(out["medication_checks"].([]map[string]any), leafletChecks(leaflets, out["medications"].([]map[string]any), time.Now(), a.Config.AllowSyntheticKnowledge)...)
	return out, nil
}
func (a *App) healthState(c *gin.Context) {
	s, err := a.state(c, actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(200, s)
}
func (a *App) conversation(c *gin.Context) {
	u := actor(c)
	var id string
	if err := a.DB.QueryRow(c, "SELECT id FROM conversations WHERE user_id=$1", u.ID).Scan(&id); err != nil {
		internalError(c, err)
		return
	}
	m, err := rows(c, a.DB, "SELECT * FROM (SELECT id,role,content,attachments,metadata,created_at FROM messages WHERE conversation_id=$1 AND user_id=$2 ORDER BY created_at DESC,id DESC LIMIT 500) recent ORDER BY created_at,id", id, u.ID)
	if err != nil {
		internalError(c, err)
		return
	}
	runs, err := rows(c, a.DB, "SELECT id,message_id,status,error_code,retry_of,created_at FROM agent_runs WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1000", u.ID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(200, gin.H{"id": id, "messages": m, "runs": runs})
}
func (a *App) systemStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.Config.AgentURL+"/healthz", nil)
	res, err := a.HTTP.Do(req)
	status := map[string]any{"agent_available": false, "model_configured": false, "test_provider": false}
	if err == nil {
		defer res.Body.Close()
		var v map[string]any
		if json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&v) == nil {
			status = v
			status["agent_available"] = res.StatusCode == 200
		}
	}
	c.JSON(200, status)
}
func (a *App) dailySummary(c *gin.Context) {
	s, err := a.state(c, actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	episodes := s["episodes"].([]map[string]any)
	active := []map[string]any{}
	archived := []map[string]any{}
	for _, e := range episodes {
		if e["status"] == "active" {
			active = append(active, e)
		} else {
			archived = append(archived, e)
		}
	}
	care := []string{"规律作息，留意身体与平时相比的变化。", "如果今天有新的不适，可以直接告诉我，也可以附上照片。"}
	mode := "new"
	if len(archived) > 0 {
		mode = "history"
		care = []string{"已结束的记录可供回顾；新的不适请重新描述，不默认继续使用旧药。", "结合近期实际记录观察反复出现的变化，有疑问时补充具体时间与感受。"}
	}
	if len(active) > 0 {
		mode = "active"
		care = []string{"记录今天的实际变化、活动影响和用药情况，记不清的内容可以保留未知。", "如不适明显加重或超出自主处理范围，请及时寻求医疗帮助。"}
	}
	profiles := s["profile"].([]map[string]any)
	if len(profiles) > 0 {
		profile, _ := profiles[0]["data"].(map[string]any)
		if list, ok := profile["allergies"].([]any); ok && len(list) > 0 {
			care = append(care, "已记录你的过敏背景。若近期环境或接触发生变化，可以一起记录，供后续回顾。")
		} else if list, ok := profile["current_medications"].([]any); ok && len(list) > 0 {
			care = append(care, "你补充过在用药背景。若实际产品或使用方式变化，可以在对话中更正。")
		}
	}
	c.JSON(200, gin.H{"insights": dailyInsights(s), "mode": mode, "active": active, "archived_count": len(archived), "observation_count": len(s["observations"].([]map[string]any)), "care": care, "weather": gin.H{"available": false, "message": "可在对话中补充城市；当前未接入实时天气。"}, "updated_at": time.Now().UTC(), "state": s})
}
func (a *App) personalExport(c *gin.Context) {
	s, err := a.state(c, actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	delete(s, "pending_actions")
	messages, err := rows(c, a.DB, "SELECT id,role,content,attachments,created_at FROM messages WHERE user_id=$1 ORDER BY created_at,id", actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	s["messages"] = messages
	a.audit(c, actor(c).ID, "personal_export", actor(c).ID, "personal")
	c.Header("Content-Disposition", "attachment; filename=health-records.json")
	c.JSON(200, gin.H{"schema_version": "1", "exported_at": time.Now().UTC(), "records": s})
}
