package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type agentResult struct {
	Reply          string           `json:"reply"`
	Actions        []proposal       `json:"actions"`
	Sources        []map[string]any `json:"sources"`
	Extracted      map[string]any   `json:"extracted"`
	Provider       string           `json:"provider"`
	Urgency        string           `json:"urgency"`
	ModelConfig    map[string]any   `json:"model_config"`
	RuntimeMetrics map[string]any   `json:"runtime_metrics"`
}
type agentEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (a *App) Work(ctx context.Context) error {
	var wg sync.WaitGroup
	loop := func(interval time.Duration, step func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					step()
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		loop(250*time.Millisecond, func() {
			id, err := a.claimRun(ctx)
			if err == nil {
				a.processRun(ctx, id)
			} else if !errors.Is(err, pgx.ErrNoRows) && ctx.Err() == nil {
				slog.Error("claim run failed", "error", err)
			}
		})
	}
	loop(time.Second, func() {
		_, _ = a.DB.Exec(ctx, "UPDATE care_tasks SET status='due',version=version+1 WHERE status='scheduled' AND due_at<=now()")
		a.resumeConfirmed(ctx)
		a.cleanupDeleted(ctx)
		exhausted, err := rows(ctx, a.DB, "UPDATE agent_runs SET status='failed',error_code='retry_exhausted',updated_at=now() WHERE status='running' AND lease_until<now() AND attempts>=3 RETURNING id")
		if err == nil {
			for _, v := range exhausted {
				a.event(ctx, fmt.Sprint(v["id"]), "error", map[string]any{"code": "retry_exhausted", "message": "处理未完成，消息已保存，请重新尝试。"})
			}
		}
	})
	<-ctx.Done()
	wg.Wait()
	return nil
}
func (a *App) claimRun(ctx context.Context) (string, error) {
	var id string
	err := a.DB.QueryRow(ctx, `WITH job AS (
 SELECT r.id FROM agent_runs r WHERE (r.status='queued' OR (r.status='running' AND r.lease_until<now())) AND r.attempts<3
 AND NOT EXISTS(SELECT 1 FROM agent_runs other WHERE other.conversation_id=r.conversation_id AND other.id<>r.id AND other.status='running' AND other.lease_until>now())
 ORDER BY r.created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE agent_runs SET status='running',attempts=attempts+1,lease_until=now()+interval '120 seconds',updated_at=now() WHERE id=(SELECT id FROM job) RETURNING id`).Scan(&id)
	return id, err
}
func (a *App) processRun(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, 100*time.Second)
	defer cancel()
	var conv string
	if err := a.DB.QueryRow(ctx, "SELECT conversation_id FROM agent_runs WHERE id=$1", id).Scan(&conv); err != nil {
		return
	}
	lock, err := a.DB.Acquire(ctx)
	if err != nil {
		a.failRun(parent, id, "storage_error")
		return
	}
	defer lock.Release()
	var locked bool
	if err = lock.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,731))", conv).Scan(&locked); err != nil || !locked {
		_, _ = a.DB.Exec(parent, "UPDATE agent_runs SET status='queued',attempts=GREATEST(attempts-1,0),lease_until=NULL WHERE id=$1 AND status='running'", id)
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := lock.Exec(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1,731))", conv); err != nil {
			_ = lock.Conn().Close(context.Background())
		}
	}()
	a.event(ctx, id, "status", map[string]any{"status": "running"})
	req, err := http.NewRequestWithContext(ctx, "POST", a.Config.AgentURL+"/internal/runs", bytes.NewReader(encode(map[string]any{"run_id": id, "tool_access": signRunAccess(a.Config.RunSigningKey, id, time.Now().Add(2*time.Minute).Unix())})))
	if err != nil {
		a.failRun(parent, id, "agent_unavailable")
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.Config.InternalToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.HTTP.Do(req)
	if err != nil {
		a.failRun(parent, id, "agent_unavailable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		a.failRun(parent, id, "agent_unavailable")
		return
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 65536), 2<<20)
	var result *agentResult
	for scanner.Scan() {
		var status string
		if e := a.DB.QueryRow(ctx, "SELECT status FROM agent_runs WHERE id=$1", id).Scan(&status); e != nil || status == "cancelled" {
			cancel()
			return
		}
		var e agentEvent
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			a.failRun(parent, id, "invalid_agent_response")
			return
		}
		switch e.Type {
		case "delta":
			var d map[string]any
			if json.Unmarshal(e.Data, &d) == nil {
				a.event(ctx, id, "delta", d)
			}
		case "node", "telemetry":
			var d map[string]any
			if json.Unmarshal(e.Data, &d) == nil {
				a.event(ctx, id, e.Type, d)
			}
		case "result":
			var r agentResult
			if json.Unmarshal(e.Data, &r) != nil {
				a.failRun(parent, id, "invalid_agent_response")
				return
			}
			result = &r
		case "error":
			var d map[string]any
			_ = json.Unmarshal(e.Data, &d)
			code, _ := d["code"].(string)
			if code == "" {
				code = "agent_failed"
			}
			a.failRun(parent, id, code)
			return
		}
	}
	if scanner.Err() != nil || result == nil {
		a.failRun(parent, id, "agent_incomplete")
		return
	}
	if len(result.Reply) == 0 || len(result.Reply) > 30000 || len(result.Actions) > 6 {
		a.failRun(parent, id, "invalid_agent_response")
		return
	}
	tx, err := a.DB.Begin(parent)
	if err != nil {
		a.failRun(parent, id, "storage_error")
		return
	}
	committed := false
	failureCode := "storage_error"
	defer func() {
		_ = tx.Rollback(parent)
		if !committed {
			a.failRun(parent, id, failureCode)
		}
	}()
	var user, status string
	err = tx.QueryRow(parent, "SELECT user_id,conversation_id,status FROM agent_runs WHERE id=$1 FOR UPDATE", id).Scan(&user, &conv, &status)
	if err != nil || status != "running" {
		return
	}
	for _, p := range result.Actions {
		if !actionKinds[p.Kind] || p.Payload == nil {
			failureCode = "invalid_action_proposal"
			return
		}
		_, err = tx.Exec(parent, "INSERT INTO actions(user_id,run_id,kind,payload,expected_version) VALUES($1,$2,$3,$4,$5) ON CONFLICT(run_id,kind) DO NOTHING", user, id, p.Kind, encode(p.Payload), p.ExpectedVersion)
		if err != nil {
			return
		}
	}
	actions, err := rows(parent, tx, "SELECT id,kind,payload,expected_version,status FROM actions WHERE run_id=$1 ORDER BY created_at", id)
	if err != nil {
		return
	}
	meta := map[string]any{"run_id": id, "actions": actions, "sources": result.Sources, "extracted": result.Extracted, "urgency": result.Urgency, "provider": result.Provider, "model_config": result.ModelConfig, "runtime_metrics": result.RuntimeMetrics}
	_, err = tx.Exec(parent, "INSERT INTO messages(conversation_id,user_id,role,content,metadata) VALUES($1,$2,'assistant',$3,$4)", conv, user, result.Reply, encode(meta))
	if err != nil {
		return
	}
	status = "succeeded"
	if len(actions) > 0 {
		status = "waiting_user"
	}
	_, err = tx.Exec(parent, "UPDATE agent_runs SET status=$1,provider=$2,lease_until=NULL,updated_at=now() WHERE id=$3", status, result.Provider, id)
	if err != nil {
		return
	}
	_, err = tx.Exec(parent, "INSERT INTO run_events(run_id,type,data) VALUES($1,'result',$2)", id, encode(map[string]any{"status": status, "reply": result.Reply, "metadata": meta}))
	if err != nil {
		return
	}
	if err = tx.Commit(parent); err != nil {
		slog.Error("run completion failed", "error", err)
	} else {
		committed = true
	}
}
func (a *App) failRun(ctx context.Context, id, code string) {
	r, err := a.DB.Exec(ctx, "UPDATE agent_runs SET status='failed',error_code=$1,lease_until=NULL,updated_at=now() WHERE id=$2 AND status IN ('queued','running')", code, id)
	if err != nil {
		slog.Error("run failure persistence", "error", err)
		return
	}
	if r.RowsAffected() == 0 {
		return
	}
	a.event(ctx, id, "error", map[string]any{"code": code, "message": publicRunError(code)})
}
func publicRunError(code string) string {
	switch code {
	case "provider_not_configured":
		return "消息已保存，AI服务尚未配置。配置后可以重新发送，记录和查询仍可使用。"
	case "model_timeout":
		return "消息已保存，AI响应超时，请稍后重试。"
	case "model_authentication_failed":
		return "AI服务配置暂不可用，消息已保存。"
	default:
		return "消息已保存，此次分析没有完成，请稍后重试。"
	}
}
func (a *App) resumeConfirmed(ctx context.Context) {
	items, err := rows(ctx, a.DB, "SELECT r.id FROM agent_runs r WHERE r.status='waiting_user' AND NOT EXISTS(SELECT 1 FROM actions a WHERE a.run_id=r.id AND a.status='pending' AND a.expires_at>now()) LIMIT 10")
	if err != nil {
		return
	}
	for _, item := range items {
		id := fmt.Sprint(item["id"])
		req, _ := http.NewRequestWithContext(ctx, "POST", a.Config.AgentURL+"/internal/runs/"+id+"/resume", bytes.NewReader([]byte(`{"resolved":true}`)))
		req.Header.Set("Authorization", "Bearer "+a.Config.InternalToken)
		req.Header.Set("Content-Type", "application/json")
		res, e := a.HTTP.Do(req)
		if e != nil {
			continue
		}
		res.Body.Close()
		if res.StatusCode == 200 {
			_, _ = a.DB.Exec(ctx, "UPDATE agent_runs SET status='succeeded',updated_at=now() WHERE id=$1 AND status='waiting_user'", id)
		}
	}
}
func (a *App) cleanupDeleted(ctx context.Context) {
	items, err := rows(ctx, a.DB, "SELECT id,user_id,conversation_id,attachment_ids FROM deletion_jobs WHERE status='queued' ORDER BY created_at LIMIT 5")
	if err != nil {
		return
	}
	for _, v := range items {
		user := fmt.Sprint(v["user_id"])
		req, _ := http.NewRequestWithContext(ctx, "DELETE", a.Config.AgentURL+"/internal/conversations/"+fmt.Sprint(v["conversation_id"]), nil)
		req.Header.Set("Authorization", "Bearer "+a.Config.InternalToken)
		res, e := a.HTTP.Do(req)
		if e != nil {
			continue
		}
		res.Body.Close()
		if res.StatusCode == 200 {
			ids := []string{}
			if values, ok := v["attachment_ids"].([]any); ok {
				for _, value := range values {
					ids = append(ids, fmt.Sprint(value))
				}
			}
			if a.removeFilesForCleanup(ctx, user, ids) == nil {
				_, _ = a.DB.Exec(ctx, "UPDATE deletion_jobs SET status='completed' WHERE id=$1", v["id"])
			}
		}
	}
}
