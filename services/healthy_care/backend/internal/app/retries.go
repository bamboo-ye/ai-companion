package app

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
)

// Retrying reuses the original user message and attachments, while a new run ID
// gives LangGraph a fresh checkpoint. retry_of is unique for request replay.
func (a *App) retryRun(c *gin.Context) {
	if !validID(c, "id") {
		return
	}
	tx, err := a.DB.Begin(c)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(c)
	if err = lockHealthWrites(c, tx, actor(c).ID); err != nil {
		internalError(c, err)
		return
	}
	var message, conversation, status string
	err = tx.QueryRow(c, "SELECT message_id,conversation_id,status FROM agent_runs WHERE id=$1 AND user_id=$2 FOR UPDATE", c.Param("id"), actor(c).ID).Scan(&message, &conversation, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		fail(c, 404, "not_found", "没有找到这次处理。")
		return
	}
	if err != nil {
		internalError(c, err)
		return
	}
	var existing string
	err = tx.QueryRow(c, "SELECT id FROM agent_runs WHERE retry_of=$1", c.Param("id")).Scan(&existing)
	if err == nil {
		c.JSON(202, gin.H{"run_id": existing, "message_id": message, "replayed": true})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		internalError(c, err)
		return
	}
	if status != "failed" && status != "cancelled" {
		fail(c, 409, "not_retryable", "这次处理仍在进行或已经完成，请刷新查看。")
		return
	}
	var pending int
	if err = tx.QueryRow(c, "SELECT count(*) FROM agent_runs WHERE user_id=$1 AND status IN ('queued','running')", actor(c).ID).Scan(&pending); err != nil {
		internalError(c, err)
		return
	}
	if pending >= 5 {
		fail(c, 429, "too_many_runs", "还有消息正在处理，请稍后再试。")
		return
	}
	var id string
	err = tx.QueryRow(c, "INSERT INTO agent_runs(user_id,conversation_id,message_id,retry_of) VALUES($1,$2,$3,$4) RETURNING id", actor(c).ID, conversation, message, c.Param("id")).Scan(&id)
	if err != nil {
		internalError(c, err)
		return
	}
	if _, err = tx.Exec(c, "INSERT INTO run_events(run_id,type,data) VALUES($1,'status','{\"status\":\"queued\"}')", id); err != nil {
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(202, gin.H{"run_id": id, "message_id": message, "replayed": false})
}
