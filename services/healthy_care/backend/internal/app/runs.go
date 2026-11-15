package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type messageInput struct {
	Content     string   `json:"content"`
	Attachments []string `json:"attachments"`
	ClientID    string   `json:"client_id"`
}

func (a *App) sendMessage(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	var in messageInput
	if !bind(c, &in) {
		return
	}
	in.Content = strings.TrimSpace(in.Content)
	if _, err := uuid.Parse(in.ClientID); err != nil || len(in.Content) > 12000 || len(in.Attachments) > 4 || (in.Content == "" && len(in.Attachments) == 0) {
		fail(c, 400, "invalid_message", "请输入文字或图片，并提供有效的消息编号。")
		return
	}
	if in.Attachments == nil {
		in.Attachments = []string{}
	}
	for i, raw := range in.Attachments {
		id, err := uuid.Parse(raw)
		if err != nil {
			fail(c, 400, "invalid_attachment", "图片编号不正确。")
			return
		}
		in.Attachments[i] = id.String()
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
	var owner string
	if err = tx.QueryRow(c, "SELECT user_id FROM conversations WHERE id=$1", c.Param("id")).Scan(&owner); errors.Is(err, pgx.ErrNoRows) || owner != user {
		fail(c, 404, "not_found", "没有找到对话。")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	if _, err = tx.Exec(c, "SELECT pg_advisory_xact_lock(hashtextextended($1,419))", user+":"+in.ClientID); err != nil {
		internalError(c, err)
		return
	}
	var existingContent, existingRun, existingMessage string
	var existingAttachments []byte
	err = tx.QueryRow(c, "SELECT m.id,m.content,m.attachments,r.id FROM messages m JOIN agent_runs r ON r.message_id=m.id WHERE m.user_id=$1 AND m.client_id=$2 ORDER BY r.created_at DESC,r.id DESC LIMIT 1", user, in.ClientID).Scan(&existingMessage, &existingContent, &existingAttachments, &existingRun)
	if err == nil {
		if existingContent != in.Content || string(existingAttachments) != string(encode(in.Attachments)) {
			var old []string
			_ = json.Unmarshal(existingAttachments, &old)
			if existingContent != in.Content || strings.Join(old, ",") != strings.Join(in.Attachments, ",") {
				fail(c, 409, "idempotency_conflict", "同一消息编号不能用于不同内容。")
				return
			}
		}
		c.JSON(202, gin.H{"message_id": existingMessage, "run_id": existingRun, "replayed": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		internalError(c, err)
		return
	}
	seen := map[string]bool{}
	for _, id := range in.Attachments {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			fail(c, 400, "invalid_attachment", "图片编号不正确或重复。")
			return
		}
		seen[id] = true
		var own string
		if err = tx.QueryRow(c, "SELECT user_id FROM attachments WHERE id=$1", id).Scan(&own); err != nil || own != user {
			fail(c, 404, "attachment_not_found", "图片不存在或不属于当前用户。")
			return
		}
	}
	var count int
	if err = tx.QueryRow(c, "SELECT count(*) FROM agent_runs WHERE user_id=$1 AND status IN ('queued','running')", user).Scan(&count); err != nil {
		internalError(c, err)
		return
	}
	if count >= 5 {
		fail(c, 429, "too_many_runs", "还有较多消息正在处理，请稍后再试。")
		return
	}
	var mid, rid string
	err = tx.QueryRow(c, "INSERT INTO messages(conversation_id,user_id,role,content,attachments,client_id) VALUES($1,$2,'user',$3,$4,$5) RETURNING id", c.Param("id"), user, in.Content, encode(in.Attachments), in.ClientID).Scan(&mid)
	if err != nil {
		internalError(c, err)
		return
	}
	err = tx.QueryRow(c, "INSERT INTO agent_runs(user_id,conversation_id,message_id) VALUES($1,$2,$3) RETURNING id", user, c.Param("id"), mid).Scan(&rid)
	if err != nil {
		internalError(c, err)
		return
	}
	_, err = tx.Exec(c, "INSERT INTO run_events(run_id,type,data) VALUES($1,'status','{\"status\":\"queued\"}')", rid)
	if err != nil {
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(202, gin.H{"message_id": mid, "run_id": rid, "replayed": false})
}
func (a *App) getRun(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	r, err := rows(c, a.DB, "SELECT r.id,r.message_id,r.status,r.error_code,r.provider,r.attempts,r.created_at,r.updated_at,COALESCE((SELECT jsonb_agg(e.data ORDER BY e.id) FROM run_events e WHERE e.run_id=r.id AND e.type='telemetry'),'[]'::jsonb) AS telemetry FROM agent_runs r WHERE r.id=$1 AND r.user_id=$2", c.Param("id"), actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	if len(r) == 0 {
		fail(c, 404, "not_found", "没有找到任务。")
		return
	}
	c.JSON(200, r[0])
}
func (a *App) cancelRun(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	r, err := a.DB.Exec(c, "UPDATE agent_runs SET status='cancelled',updated_at=now() WHERE id=$1 AND user_id=$2 AND status IN ('queued','running')", c.Param("id"), actor(c).ID)
	if err != nil {
		internalError(c, err)
		return
	}
	if r.RowsAffected() == 0 {
		fail(c, 409, "not_cancellable", "任务已结束或不可取消。")
		return
	}
	a.event(c, c.Param("id"), "status", map[string]any{"status": "cancelled"})
	c.JSON(200, gin.H{"ok": true})
}
func (a *App) event(ctx context.Context, id, kind string, data any) {
	if _, err := a.DB.Exec(ctx, "INSERT INTO run_events(run_id,type,data) VALUES($1,$2,$3)", id, kind, encode(data)); err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Println("run event persistence failed")
		}
	}
}
func (a *App) streamRun(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	var exists bool
	if err := a.DB.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND user_id=$2)", c.Param("id"), actor(c).ID).Scan(&exists); err != nil {
		internalError(c, err)
		return
	}
	if !exists {
		fail(c, 404, "not_found", "没有找到任务。")
		return
	}
	last, _ := strconv.ParseInt(c.GetHeader("Last-Event-ID"), 10, 64)
	if last == 0 {
		last, _ = strconv.ParseInt(c.Query("after"), 10, 64)
	}
	if last < 0 {
		last = 0
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(200)
	// Send the headers immediately so proxies and the client can establish
	// the stream even when no new events are available yet.
	c.Writer.Flush()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(110 * time.Second)
	defer deadline.Stop()
	for {
		ev, err := rows(c.Request.Context(), a.DB, "SELECT id,type,data FROM run_events WHERE run_id=$1 AND id>$2 ORDER BY id LIMIT 100", c.Param("id"), last)
		if err != nil {
			return
		}
		for _, v := range ev {
			last = v["id"].(int64)
			fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", last, v["type"], encode(v["data"]))
			c.Writer.Flush()
		}
		if len(ev) == 100 {
			continue
		}
		var status string
		if err = a.DB.QueryRow(c.Request.Context(), "SELECT status FROM agent_runs WHERE id=$1", c.Param("id")).Scan(&status); err != nil {
			return
		}
		if status == "succeeded" || status == "failed" || status == "cancelled" || status == "waiting_user" {
			fmt.Fprintf(c.Writer, "event: end\ndata: {\"status\":%q}\n\n", status)
			c.Writer.Flush()
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

func (a *App) contextForRun(ctx context.Context, id string) (map[string]any, error) {
	var user, conv, mid, content string
	var at time.Time
	err := a.DB.QueryRow(ctx, "SELECT r.user_id,r.conversation_id,r.message_id,m.content,m.created_at FROM agent_runs r JOIN messages m ON m.id=r.message_id WHERE r.id=$1", id).Scan(&user, &conv, &mid, &content, &at)
	if err != nil {
		return nil, err
	}
	s, err := a.state(ctx, user)
	if err != nil {
		return nil, err
	}
	messages, err := rows(ctx, a.DB, "SELECT id,role,content,attachments,metadata FROM (SELECT id,role,content,attachments,metadata,created_at FROM messages WHERE conversation_id=$1 AND created_at<=$2 ORDER BY created_at DESC,id DESC LIMIT 30) t ORDER BY created_at,id", conv, at)
	if err != nil {
		return nil, err
	}
	knowledge, err := a.referenceKnowledge(ctx)
	if err != nil {
		return nil, err
	}
	leaflets, err := loadLeaflets(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	catalog := []map[string]any{}
	for _, p := range leaflets {
		if published(p, time.Now(), a.Config.AllowSyntheticKnowledge) {
			catalog = append(catalog, leafletMetadata(p))
			knowledge = append(knowledge, map[string]any{"id": "product:" + p.ID + ":" + p.Version, "topic": "产品说明", "title": p.Name + " · " + p.Strength, "content": p.Information, "source_url": p.SourceURL, "version": p.Version, "review_status": p.ReviewStatus})
		}
	}
	loc, err := userLocation(ctx, a.DB, user)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run_id": id, "conversation_id": conv, "message_id": mid, "content": content, "messages": messages, "state": s, "knowledge": knowledge, "catalog": catalog, "now": time.Now().UTC().Format(time.RFC3339), "timezone": loc.String()}, nil
}
func (a *App) runContext(c *gin.Context) {
	if !a.requireRunAccess(c) {
		return
	}
	if !validID(c, "id") {
		return
	}
	v, err := a.contextForRun(c, c.Param("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, 404, "not_found", "没有找到任务。")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(200, v)
}
func (a *App) runAttachment(c *gin.Context) {
	if !a.requireRunAccess(c) {
		return
	}
	if !validID(c, "id") || !validID(c, "attachment") {
		return
	}
	var user, ctype string
	err := a.DB.QueryRow(c, `SELECT a.user_id,a.content_type FROM attachments a JOIN agent_runs r ON r.user_id=a.user_id JOIN messages m ON m.id=r.message_id WHERE r.id=$1 AND a.id=$2::uuid AND m.attachments ? $2::text`, c.Param("id"), c.Param("attachment")).Scan(&user, &ctype)
	if err != nil {
		fail(c, 404, "not_found", "没有找到图片。")
		return
	}
	b, err := os.ReadFile(filepath.Join(a.Config.StorageDir, user, c.Param("attachment")))
	if err != nil {
		fail(c, 404, "not_found", "图片暂不可用。")
		return
	}
	c.JSON(200, gin.H{"data_url": "data:" + ctype + ";base64," + base64.StdEncoding.EncodeToString(b)})
}

// Keep the compiler explicit about the request/stream APIs used by adapters.
var _ io.Reader
var _ = http.MethodPost
