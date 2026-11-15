package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var actionKinds = map[string]bool{"create_episode": true, "checkin": true, "record_medication": true, "update_medication": true, "archive_episode": true, "update_profile": true, "create_task": true, "update_task": true, "delete_records": true}

type proposal struct {
	Kind            string         `json:"kind"`
	Payload         map[string]any `json:"payload"`
	ExpectedVersion *int           `json:"expected_version,omitempty"`
}
type actionRecord struct {
	ID, Kind, Status string
	Payload          map[string]any
	ExpectedVersion  *int
	Expires          time.Time
	Result           []byte
	MessageID        string
}
type domainError struct {
	Status        int
	Code, Message string
}

func (e *domainError) Error() string { return e.Message }
func invalid(message string) error   { return &domainError{400, "invalid_action", message} }
func conflict() error {
	return &domainError{409, "version_conflict", "记录已更新，请重新查看并确认新的内容。"}
}
func text(m map[string]any, k string) string            { v, _ := m[k].(string); return strings.TrimSpace(v) }
func number(m map[string]any, k string) (float64, bool) { v, ok := m[k].(float64); return v, ok }
func knownFields(m map[string]any, allowed ...string) bool {
	for k := range m {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (a *App) confirmAction(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	user := actor(c).ID
	tx, err := a.DB.Begin(c)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(c)
	if err = lockHealthWrites(c, tx, user); err != nil {
		internalError(c, err)
		return
	}
	var receipt []byte
	if err = tx.QueryRow(c, "SELECT result FROM action_receipts WHERE action_id=$1 AND user_id=$2", c.Param("id"), user).Scan(&receipt); err == nil {
		var result any
		_ = json.Unmarshal(receipt, &result)
		c.JSON(200, gin.H{"result": result, "replayed": true})
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		internalError(c, err)
		return
	}
	var x actionRecord
	var raw []byte
	err = tx.QueryRow(c, `SELECT a.id,a.kind,a.payload,a.expected_version,a.status,a.expires_at,a.result,r.message_id
FROM actions a JOIN agent_runs r ON r.id=a.run_id WHERE a.id=$1 AND a.user_id=$2 FOR UPDATE OF a`, c.Param("id"), user).Scan(&x.ID, &x.Kind, &raw, &x.ExpectedVersion, &x.Status, &x.Expires, &x.Result, &x.MessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, 404, "not_found", "没有找到待确认操作。")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	if x.Status == "confirmed" {
		var r any
		_ = json.Unmarshal(x.Result, &r)
		c.JSON(200, gin.H{"result": r, "replayed": true})
		return
	}
	if x.Status != "pending" || time.Now().After(x.Expires) {
		fail(c, 409, "action_expired", "此操作已失效，请重新说明需求。")
		return
	}
	if err = json.Unmarshal(raw, &x.Payload); err != nil {
		internalError(c, err)
		return
	}
	result, err := a.applyAction(c, tx, user, x)
	if err != nil {
		var d *domainError
		if errors.As(err, &d) {
			fail(c, d.Status, d.Code, d.Message)
		} else {
			internalError(c, err)
		}
		return
	}
	if x.Kind != "delete_records" {
		_, err = tx.Exec(c, "UPDATE actions SET status='confirmed',result=$1 WHERE id=$2", encode(result), x.ID)
		if err != nil {
			internalError(c, err)
			return
		}
	}
	if _, err = tx.Exec(c, "INSERT INTO action_receipts(action_id,user_id,result) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", x.ID, user, encode(result)); err != nil {
		internalError(c, err)
		return
	}
	_, err = tx.Exec(c, "INSERT INTO audit_events(actor_id,action,resource_id,purpose) VALUES($1,$2,$3,'personal')", user, "confirm_"+x.Kind, x.ID)
	if err != nil {
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		internalError(c, err)
		return
	}
	_ = a.Cache.Del(c, "daily:"+hash(user)).Err()
	c.JSON(200, gin.H{"result": result, "replayed": false})
}
func (a *App) rejectAction(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	r, err := a.DB.Exec(c, "UPDATE actions SET status='rejected' WHERE id=$1 AND user_id=$2 AND status='pending'", c.Param("id"), actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	if r.RowsAffected() == 0 {
		fail(c, 404, "not_found", "没有找到待确认操作。")
		return
	}
	c.JSON(200, gin.H{"ok": true})
}

func lockEpisode(c *gin.Context, tx pgx.Tx, user string, x actionRecord) (string, error) {
	id := text(x.Payload, "episode_id")
	if _, err := uuid.Parse(id); err != nil {
		return "", invalid("请先确认要更新的病程。")
	}
	var version int
	var status string
	err := tx.QueryRow(c, "SELECT version,status FROM episodes WHERE id=$1 AND user_id=$2 FOR UPDATE", id, user).Scan(&version, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &domainError{404, "not_found", "没有找到对应病程。"}
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", &domainError{409, "episode_archived", "这段病程已经归档，请重新描述当前不适。"}
	}
	if x.ExpectedVersion == nil || version != *x.ExpectedVersion {
		return "", conflict()
	}
	return id, nil
}

func (a *App) applyAction(c *gin.Context, tx pgx.Tx, user string, x actionRecord) (map[string]any, error) {
	p := x.Payload
	if err := validatePayload(x.Kind, p); err != nil {
		return nil, err
	}
	out := map[string]any{"kind": x.Kind, "saved": true}
	switch x.Kind {
	case "create_episode":
		if !knownFields(p, "complaint", "topic", "started_at") || text(p, "complaint") == "" || len(text(p, "complaint")) > 4000 {
			return nil, invalid("请补充具体的不适描述。")
		}
		started := time.Now().UTC()
		if v := text(p, "started_at"); v != "" {
			t, e := time.Parse(time.RFC3339, v)
			if e != nil || t.After(time.Now().Add(24*time.Hour)) {
				return nil, invalid("开始时间不正确。")
			}
			started = t
		}
		topic := text(p, "topic")
		if topic == "" {
			topic = "其他不适"
		}
		if len(topic) > 100 {
			return nil, invalid("症状主题过长。")
		}
		var id string
		err := tx.QueryRow(c, "INSERT INTO episodes(user_id,topic,complaint,started_at,source_message_id,onset_known) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", user, topic, text(p, "complaint"), started, x.MessageID, text(p, "started_at") != "").Scan(&id)
		if err != nil {
			return nil, err
		}
		out["episode_id"] = id
	case "checkin":
		if !knownFields(p, "episode_id", "observed_date", "score", "medication_state", "activity", "note", "symptoms") {
			return nil, invalid("随访字段不正确。")
		}
		id, err := lockEpisode(c, tx, user, x)
		if err != nil {
			return nil, err
		}
		date := text(p, "observed_date")
		loc, err := userLocation(c, tx, user)
		if err != nil {
			return nil, err
		}
		if date == "" {
			date = time.Now().In(loc).Format("2006-01-02")
		}
		d, err := time.Parse("2006-01-02", date)
		if err != nil || d.After(time.Now().In(loc).AddDate(0, 0, 1)) {
			return nil, invalid("观察日期不正确。")
		}
		if v, exists := p["score"]; exists && v != nil {
			n, ok := number(p, "score")
			if !ok || n < 0 || n > 10 {
				return nil, invalid("不适自评分应为0至10，未提供时保留空缺。")
			}
		}
		if st := text(p, "medication_state"); st != "" && st != "used" && st != "not_used" && st != "unknown" {
			return nil, invalid("用药状态不正确。")
		}
		delete(p, "episode_id")
		delete(p, "observed_date")
		_, err = tx.Exec(c, "INSERT INTO observation_revisions(observation_id,data,version) SELECT id,data,version FROM observations WHERE episode_id=$1 AND observed_date=$2", id, date)
		if err != nil {
			return nil, err
		}
		var oid string
		err = tx.QueryRow(c, `INSERT INTO observations(episode_id,user_id,observed_date,data,source_message_id) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(episode_id,observed_date) DO UPDATE SET data=observations.data||EXCLUDED.data,version=observations.version+1,source_message_id=EXCLUDED.source_message_id,recorded_at=now() RETURNING id`, id, user, date, encode(p), x.MessageID).Scan(&oid)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(c, "UPDATE episodes SET version=version+1,updated_at=now() WHERE id=$1", id)
		if err != nil {
			return nil, err
		}
		out["observation_id"] = oid
	case "record_medication":
		if !knownFields(p, "episode_id", "name", "ingredients", "strength", "route", "amount", "frequency", "used_at", "state", "basis", "note", "product_id", "leaflet_version", "region", "form") {
			return nil, invalid("用药字段不正确。")
		}
		if text(p, "name") == "" || len(text(p, "name")) > 300 {
			return nil, invalid("请核对具体产品名称。")
		}
		state := text(p, "state")
		if state != "used" && state != "not_used" && state != "unknown" {
			return nil, invalid("请明确实际使用状态。")
		}
		var eid any
		if v := text(p, "episode_id"); v != "" {
			id, err := lockEpisode(c, tx, user, x)
			if err != nil {
				return nil, err
			}
			eid = id
		}
		delete(p, "episode_id")
		if err := validateProductBinding(c, tx, p); err != nil {
			return nil, err
		}
		if ingredients, ok := p["ingredients"]; ok {
			items, ok := ingredients.([]any)
			if !ok || len(items) > 20 {
				return nil, invalid("成分列表不正确。")
			}
			for _, v := range items {
				if s, ok := v.(string); !ok || len(s) > 150 {
					return nil, invalid("请核对成分文字。")
				}
			}
		}
		var id string
		err := tx.QueryRow(c, "INSERT INTO medication_uses(user_id,episode_id,data,source_message_id) VALUES($1,$2,$3,$4) RETURNING id", user, eid, encode(p), x.MessageID).Scan(&id)
		if err != nil {
			return nil, err
		}
		out["medication_id"] = id
	case "update_medication":
		if !knownFields(p, "medication_id", "name", "ingredients", "strength", "route", "amount", "frequency", "used_at", "state", "basis", "note", "product_id", "leaflet_version", "region", "form") {
			return nil, invalid("用药更正字段不正确。")
		}
		id := text(p, "medication_id")
		if _, err := uuid.Parse(id); err != nil {
			return nil, invalid("用药记录编号不正确。")
		}
		var ver int
		var original []byte
		err := tx.QueryRow(c, "SELECT data,version FROM medication_uses WHERE id=$1 AND user_id=$2 FOR UPDATE", id, user).Scan(&original, &ver)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &domainError{404, "not_found", "没有找到用药记录。"}
		}
		if err != nil {
			return nil, err
		}
		if x.ExpectedVersion == nil || ver != *x.ExpectedVersion {
			return nil, conflict()
		}
		merged := map[string]any{}
		if err = json.Unmarshal(original, &merged); err != nil {
			return nil, err
		}
		for key, value := range p {
			if key != "medication_id" {
				merged[key] = value
			}
		}
		if err := validateProductBinding(c, tx, merged); err != nil {
			return nil, err
		}
		if text(merged, "name") == "" || len(text(merged, "name")) > 300 {
			return nil, invalid("请核对产品名称。")
		}
		st := text(merged, "state")
		if st != "used" && st != "not_used" && st != "unknown" {
			return nil, invalid("请明确实际使用状态。")
		}
		if values, exists := merged["ingredients"]; exists {
			list, ok := values.([]any)
			if !ok || len(list) > 20 {
				return nil, invalid("成分列表不正确。")
			}
			for _, value := range list {
				if str, ok := value.(string); !ok || len(str) > 150 {
					return nil, invalid("成分文字不正确。")
				}
			}
		}
		if _, err = tx.Exec(c, "INSERT INTO medication_revisions(medication_id,data,version) VALUES($1,$2,$3)", id, original, ver); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(c, "UPDATE medication_uses SET data=$1,version=version+1,source_message_id=$2,recorded_at=now() WHERE id=$3", encode(merged), x.MessageID, id); err != nil {
			return nil, err
		}
		out["medication_id"] = id
	case "archive_episode":
		if !knownFields(p, "episode_id", "reason") {
			return nil, invalid("结束跟进字段不正确。")
		}
		id, err := lockEpisode(c, tx, user, x)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(c, "UPDATE episodes SET status='archived',archive_reason=$1,version=version+1,updated_at=now() WHERE id=$2", text(p, "reason"), id)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(c, "UPDATE care_tasks SET status='paused',version=version+1 WHERE episode_id=$1 AND user_id=$2 AND status IN ('scheduled','due')", id, user)
		if err != nil {
			return nil, err
		}
		out["episode_id"] = id
	case "update_profile":
		if !knownFields(p, "age", "age_range", "allergies", "conditions", "current_medications", "city", "timezone", "pregnancy_status", "region") {
			return nil, invalid("健康背景字段不正确。")
		}
		if age, ok := number(p, "age"); ok && (age < 0 || age > 120) {
			return nil, invalid("年龄不正确。")
		}
		if zone := text(p, "timezone"); zone != "" {
			if _, err := time.LoadLocation(zone); err != nil {
				return nil, invalid("请使用有效的IANA时区，例如Asia/Shanghai。")
			}
		}
		var version int
		if err := tx.QueryRow(c, "SELECT version FROM profiles WHERE user_id=$1 FOR UPDATE", user).Scan(&version); err != nil {
			return nil, err
		}
		if x.ExpectedVersion == nil || version != *x.ExpectedVersion {
			return nil, conflict()
		}
		_, err := tx.Exec(c, "UPDATE profiles SET data=data||$1::jsonb,version=version+1,updated_at=now() WHERE user_id=$2", encode(p), user)
		if err != nil {
			return nil, err
		}
	case "create_task":
		if !knownFields(p, "episode_id", "title", "due_at", "recurrence") {
			return nil, invalid("提醒字段不正确。")
		}
		due, err := time.Parse(time.RFC3339, text(p, "due_at"))
		if err != nil || due.After(time.Now().AddDate(1, 0, 0)) {
			return nil, invalid("请明确有效的提醒时间。")
		}
		title := text(p, "title")
		if title == "" || len(title) > 500 {
			return nil, invalid("请明确提醒内容。")
		}
		rec := text(p, "recurrence")
		if rec == "" {
			rec = "once"
		}
		if rec != "once" && rec != "daily" {
			return nil, invalid("提醒重复方式不正确。")
		}
		var eid any
		if text(p, "episode_id") != "" {
			id, e := lockEpisode(c, tx, user, x)
			if e != nil {
				return nil, e
			}
			eid = id
		}
		var id string
		err = tx.QueryRow(c, "INSERT INTO care_tasks(user_id,episode_id,title,due_at,recurrence) VALUES($1,$2,$3,$4,$5) RETURNING id", user, eid, title, due, rec).Scan(&id)
		if err != nil {
			return nil, err
		}
		out["task_id"] = id
	case "update_task":
		if !knownFields(p, "task_id", "status", "due_at") {
			return nil, invalid("提醒更新字段不正确。")
		}
		id := text(p, "task_id")
		if _, err := uuid.Parse(id); err != nil {
			return nil, invalid("提醒编号不正确。")
		}
		st := text(p, "status")
		if st != "scheduled" && st != "completed" && st != "paused" {
			return nil, invalid("提醒状态不正确。")
		}
		var ver int
		var recurrence string
		var due time.Time
		err := tx.QueryRow(c, "SELECT version,recurrence,due_at FROM care_tasks WHERE id=$1 AND user_id=$2 FOR UPDATE", id, user).Scan(&ver, &recurrence, &due)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &domainError{404, "not_found", "没有找到提醒。"}
		}
		if err != nil {
			return nil, err
		}
		if x.ExpectedVersion == nil || ver != *x.ExpectedVersion {
			return nil, conflict()
		}
		if v := text(p, "due_at"); v != "" {
			due, err = time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, invalid("提醒时间不正确。")
			}
		}
		if st == "completed" && recurrence == "daily" {
			loc, err := userLocation(c, tx, user)
			if err != nil {
				return nil, err
			}
			due = due.In(loc).AddDate(0, 0, 1)
			for !due.After(time.Now()) {
				due = due.AddDate(0, 0, 1)
			}
			st = "scheduled"
		}
		_, err = tx.Exec(c, "UPDATE care_tasks SET status=$1,due_at=$2,version=version+1 WHERE id=$3", st, due, id)
		if err != nil {
			return nil, err
		}
	case "delete_records":
		if text(p, "scope") != "health" || !knownFields(p, "scope") {
			return nil, invalid("请明确删除健康记录的范围。")
		}
		if err := clearHealthRecords(c, tx, user); err != nil {
			return nil, err
		}
		out["cleanup_status"] = "queued"
	default:
		return nil, invalid("不支持此操作。")
	}
	return out, nil
}
func clearHealthRecords(c *gin.Context, tx pgx.Tx, user string) error {
	var conv string
	if err := tx.QueryRow(c, "SELECT id FROM conversations WHERE user_id=$1 FOR UPDATE", user).Scan(&conv); err != nil {
		return err
	}
	oldAttachments, err := rows(c, tx, "SELECT id FROM attachments WHERE user_id=$1", user)
	if err != nil {
		return err
	}
	fileIDs := []string{}
	for _, f := range oldAttachments {
		fileIDs = append(fileIDs, fmt.Sprint(f["id"]))
	}
	_, err = tx.Exec(c, "INSERT INTO deletion_jobs(user_id,conversation_id,attachment_ids) VALUES($1,$2,$3)", user, conv, encode(fileIDs))
	if err != nil {
		return err
	}
	for _, table := range []string{"episodes", "medication_uses", "care_tasks", "attachments", "conversations"} {
		if _, err = tx.Exec(c, fmt.Sprintf("DELETE FROM %s WHERE user_id=$1", table), user); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(c, "DELETE FROM action_receipts WHERE user_id=$1", user); err != nil {
		return err
	}
	if _, err = tx.Exec(c, "UPDATE profiles SET data='{}',version=version+1 WHERE user_id=$1", user); err != nil {
		return err
	}
	if _, err = tx.Exec(c, "INSERT INTO conversations(user_id) VALUES($1)", user); err != nil {
		return err
	}
	return nil
}

// Data erasure remains available even if the dialogue provider is disabled.
func (a *App) clearRecords(c *gin.Context) {
	var input struct {
		Confirmed bool `json:"confirmed"`
	}
	if !bind(c, &input) {
		return
	}
	if !input.Confirmed {
		fail(c, 400, "confirmation_required", "请确认是否清除健康记录。")
		return
	}
	user := actor(c).ID
	tx, err := a.DB.Begin(c)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(c)
	if err = lockHealthWrites(c, tx, user); err != nil {
		internalError(c, err)
		return
	}
	if err = clearHealthRecords(c, tx, user); err != nil {
		internalError(c, err)
		return
	}
	if _, err = tx.Exec(c, "INSERT INTO audit_events(actor_id,action,resource_id,purpose) VALUES($1,'confirm_delete_records',CAST($1::uuid AS text),'personal')", user); err != nil {
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		internalError(c, err)
		return
	}
	_ = a.Cache.Del(c, "daily:"+hash(user)).Err()
	c.Status(204)
}
