package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type tableSpec struct {
	name       string
	target     string
	columns    []string
	sourceRows string
	sourceKeys string
	targetKeys string
	normalize  func([]any) error
}

func main() {
	mysqlDSN := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	postgresDSN := strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
	if mysqlDSN == "" || postgresDSN == "" {
		log.Fatal("MYSQL_DSN and POSTGRES_DSN are required")
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("CORE_DATA_MIGRATION_MODE")))
	if mode == "" {
		mode = "copy"
	}
	if mode != "copy" && mode != "validate" {
		log.Fatal("CORE_DATA_MIGRATION_MODE must be copy or validate")
	}
	scope := strings.ToLower(strings.TrimSpace(os.Getenv("DATA_MIGRATION_SCOPE")))
	if scope == "" {
		scope = "core"
	}
	var specs []tableSpec
	switch scope {
	case "core":
		specs = coreTables()
	case "life":
		specs = lifeTables()
	case "tools":
		specs = toolTables()
	case "governance":
		specs = governanceTables()
	case "all":
		specs = append(coreTables(), lifeTables()...)
		specs = append(specs, toolTables()...)
		specs = append(specs, governanceTables()...)
	default:
		log.Fatal("DATA_MIGRATION_SCOPE must be core, life, tools, governance, or all")
	}

	source, target, err := openDatabases(mysqlDSN, postgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer source.Close()
	defer target.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if mode == "copy" {
		if err = copyCoreData(ctx, source, target, specs); err != nil {
			log.Fatal(err)
		}
	}
	if err = validateCoreData(ctx, source, target, specs); err != nil {
		log.Fatal(err)
	}
	if scope == "core" || scope == "all" {
		if err = validateMemoryAggregates(ctx, source, target); err != nil {
			log.Fatal(err)
		}
	}
	if scope == "life" || scope == "all" {
		if err = validateLifeAggregates(ctx, source, target); err != nil {
			log.Fatal(err)
		}
	}
	if scope == "tools" || scope == "all" {
		if err = validateToolAggregates(ctx, source, target); err != nil {
			log.Fatal(err)
		}
	}
	if scope == "governance" || scope == "all" {
		if err = validateGovernanceAggregates(ctx, source, target); err != nil {
			log.Fatal(err)
		}
	}
}

func openDatabases(mysqlDSN, postgresDSN string) (*sql.DB, *sql.DB, error) {
	parsed, err := mysql.ParseDSN(mysqlDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("parse MYSQL_DSN: %w", err)
	}
	parsed.ParseTime = true
	source, err := sql.Open("mysql", parsed.FormatDSN())
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	target, err := sql.Open("pgx", postgresDSN)
	if err != nil {
		source.Close()
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}
	return source, target, nil
}

func copyCoreData(ctx context.Context, source, target *sql.DB, specs []tableSpec) error {
	sourceTx, err := source.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin mysql snapshot: %w", err)
	}
	defer sourceTx.Rollback()
	targetTx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin postgres copy: %w", err)
	}
	defer targetTx.Rollback()

	for _, spec := range specs {
		copied, copyErr := copyTable(ctx, sourceTx, targetTx, spec)
		if copyErr != nil {
			return fmt.Errorf("copy %s: %w", spec.name, copyErr)
		}
		switch spec.name {
		case "refresh_sessions":
			copyErr = restoreSelfReferences(
				ctx, sourceTx, targetTx,
				`SELECT BIN_TO_UUID(id),BIN_TO_UUID(rotated_to_id) FROM refresh_sessions WHERE rotated_to_id IS NOT NULL`,
				`UPDATE app.refresh_sessions SET rotated_to_id=$2 WHERE id=$1`,
			)
		case "messages":
			copyErr = restoreSelfReferences(
				ctx, sourceTx, targetTx,
				`SELECT BIN_TO_UUID(id),BIN_TO_UUID(reply_to_id) FROM messages WHERE reply_to_id IS NOT NULL`,
				`UPDATE app.messages SET reply_to_id=$2 WHERE id=$1`,
			)
		case "long_term_memories":
			copyErr = restoreSelfReferences(
				ctx, sourceTx, targetTx,
				`SELECT BIN_TO_UUID(id),BIN_TO_UUID(supersedes_id) FROM long_term_memories WHERE supersedes_id IS NOT NULL`,
				`UPDATE app.long_term_memories SET supersedes_id=$2 WHERE id=$1`,
			)
		}
		if copyErr != nil {
			return fmt.Errorf("restore %s self references: %w", spec.name, copyErr)
		}
		fmt.Printf("copied %s rows=%d\n", spec.name, copied)
	}
	for _, statement := range sequenceResetStatements(specs) {
		if _, err = targetTx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reset postgres sequence: %w", err)
		}
	}
	for _, statement := range finalizationStatements(specs) {
		if _, err = targetTx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("finalize postgres constraints: %w", err)
		}
	}
	if err = targetTx.Commit(); err != nil {
		return fmt.Errorf("commit postgres copy: %w", err)
	}
	return nil
}

func restoreSelfReferences(ctx context.Context, source *sql.Tx, target *sql.Tx, sourceQuery, targetUpdate string) error {
	rows, err := source.QueryContext(ctx, sourceQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var child, parent any
		if err = rows.Scan(&child, &parent); err != nil {
			return err
		}
		if bytes, ok := child.([]byte); ok {
			child = string(bytes)
		}
		if bytes, ok := parent.([]byte); ok {
			parent = string(bytes)
		}
		if _, err = target.ExecContext(ctx, targetUpdate, child, parent); err != nil {
			return err
		}
	}
	return rows.Err()
}

func copyTable(ctx context.Context, source *sql.Tx, target *sql.Tx, spec tableSpec) (int, error) {
	rows, err := source.QueryContext(ctx, spec.sourceRows)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	placeholders := make([]string, len(spec.columns))
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	insert := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		spec.target, strings.Join(spec.columns, ","), strings.Join(placeholders, ","),
	)
	count := 0
	for rows.Next() {
		values := make([]any, len(spec.columns))
		destinations := make([]any, len(values))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err = rows.Scan(destinations...); err != nil {
			return count, err
		}
		for i, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[i] = string(bytes)
			}
		}
		if spec.normalize != nil {
			if err = spec.normalize(values); err != nil {
				return count, err
			}
		}
		if _, err = target.ExecContext(ctx, insert, values...); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

func validateCoreData(ctx context.Context, source, target *sql.DB, specs []tableSpec) error {
	var problems []string
	for _, spec := range specs {
		sourceKeys, err := readKeys(ctx, source, spec.sourceKeys)
		if err != nil {
			return fmt.Errorf("read mysql %s keys: %w", spec.name, err)
		}
		targetKeys, err := readKeys(ctx, target, spec.targetKeys)
		if err != nil {
			return fmt.Errorf("read postgres %s keys: %w", spec.name, err)
		}
		missing, extra := compareKeys(sourceKeys, targetKeys)
		fmt.Printf(
			"validated %s source=%d target=%d missing=%d extra=%d\n",
			spec.name, len(sourceKeys), len(targetKeys), len(missing), len(extra),
		)
		if len(missing) > 0 || len(extra) > 0 {
			problems = append(problems, fmt.Sprintf(
				"%s missing=%s extra=%s",
				spec.name, preview(missing), preview(extra),
			))
		}
	}
	if len(problems) > 0 {
		return errors.New("data validation failed: " + strings.Join(problems, "; "))
	}
	fmt.Printf("validated %d migration tables\n", len(specs))
	return nil
}

func validateLifeAggregates(ctx context.Context, source, target *sql.DB) error {
	checks := []struct {
		name        string
		sourceQuery string
		targetQuery string
	}{
		{
			name: "ledger active totals",
			sourceQuery: `
				SELECT CONCAT(
					BIN_TO_UUID(user_id),'|',currency,'|',direction,'|',
					COUNT(*),'|',COALESCE(SUM(amount_minor),0)
				)
				FROM ledger_entries
				WHERE status='active'
				GROUP BY user_id,currency,direction`,
			targetQuery: `
				SELECT
					user_id::text || '|' || currency || '|' || direction || '|' ||
					COUNT(*)::text || '|' || COALESCE(SUM(amount_minor),0)::text
				FROM app.ledger_entries
				WHERE status='active'
				GROUP BY user_id,currency,direction`,
		},
		{
			name: "plan item states",
			sourceQuery: `
				SELECT CONCAT(status,'|',COUNT(*))
				FROM plan_items
				GROUP BY status`,
			targetQuery: `
				SELECT status || '|' || COUNT(*)::text
				FROM app.plan_items
				GROUP BY status`,
		},
		{
			name: "reminder states",
			sourceQuery: `
				SELECT CONCAT(status,'|',COALESCE(time_precision,''),'|',COUNT(*))
				FROM reminders
				GROUP BY status,time_precision`,
			targetQuery: `
				SELECT status || '|' || COALESCE(time_precision,'') || '|' || COUNT(*)::text
				FROM app.reminders
				GROUP BY status,time_precision`,
		},
		{
			name: "notification states",
			sourceQuery: `
				SELECT CONCAT(status,'|',channel,'|',COUNT(*))
				FROM notification_deliveries
				GROUP BY status,channel`,
			targetQuery: `
				SELECT status || '|' || channel || '|' || COUNT(*)::text
				FROM app.notification_deliveries
				GROUP BY status,channel`,
		},
	}
	for _, check := range checks {
		sourceValues, err := readKeys(ctx, source, check.sourceQuery)
		if err != nil {
			return fmt.Errorf("read mysql %s: %w", check.name, err)
		}
		targetValues, err := readKeys(ctx, target, check.targetQuery)
		if err != nil {
			return fmt.Errorf("read postgres %s: %w", check.name, err)
		}
		missing, extra := compareKeys(sourceValues, targetValues)
		fmt.Printf(
			"validated %s source=%d target=%d missing=%d extra=%d\n",
			check.name, len(sourceValues), len(targetValues), len(missing), len(extra),
		)
		if len(missing) > 0 || len(extra) > 0 {
			return fmt.Errorf(
				"%s aggregate mismatch: missing=%s extra=%s",
				check.name, preview(missing), preview(extra),
			)
		}
	}
	return nil
}

func validateMemoryAggregates(ctx context.Context, source, target *sql.DB) error {
	sourceValues, err := readKeys(ctx, source, `
		SELECT CONCAT(memory_type,'|',status,'|',pinned,'|',COUNT(*))
		FROM long_term_memories
		GROUP BY memory_type,status,pinned`)
	if err != nil {
		return fmt.Errorf("read mysql memory states: %w", err)
	}
	targetValues, err := readKeys(ctx, target, `
		SELECT memory_type || '|' || status || '|' || pinned::int::text || '|' ||
			COUNT(*)::text
		FROM app.long_term_memories
		GROUP BY memory_type,status,pinned`)
	if err != nil {
		return fmt.Errorf("read postgres memory states: %w", err)
	}
	missing, extra := compareKeys(sourceValues, targetValues)
	fmt.Printf(
		"validated memory states source=%d target=%d missing=%d extra=%d\n",
		len(sourceValues), len(targetValues), len(missing), len(extra),
	)
	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf(
			"memory aggregate mismatch: missing=%s extra=%s",
			preview(missing), preview(extra),
		)
	}
	return nil
}

func validateToolAggregates(ctx context.Context, source, target *sql.DB) error {
	checks := []struct {
		name        string
		sourceQuery string
		targetQuery string
	}{
		{
			name: "document states",
			sourceQuery: `
				SELECT CONCAT(ingest_status,'|',COUNT(*),'|',SUM(page_count),'|',SUM(chunk_count))
				FROM documents
				GROUP BY ingest_status`,
			targetQuery: `
				SELECT ingest_status || '|' || COUNT(*)::text || '|' ||
					SUM(page_count)::text || '|' || SUM(chunk_count)::text
				FROM app.documents
				GROUP BY ingest_status`,
		},
		{
			name: "document job states",
			sourceQuery: `
				SELECT CONCAT(status,'|',COUNT(*),'|',SUM(attempts))
				FROM document_ingest_jobs
				GROUP BY status`,
			targetQuery: `
				SELECT status || '|' || COUNT(*)::text || '|' || SUM(attempts)::text
				FROM app.document_ingest_jobs
				GROUP BY status`,
		},
		{
			name: "skill run states",
			sourceQuery: `
				SELECT CONCAT(execution_mode,'|',status,'|',COUNT(*),'|',SUM(attempt))
				FROM skill_runs
				GROUP BY execution_mode,status`,
			targetQuery: `
				SELECT execution_mode || '|' || status || '|' ||
					COUNT(*)::text || '|' || SUM(attempt)::text
				FROM app.skill_runs
				GROUP BY execution_mode,status`,
		},
		{
			name: "tool execution states",
			sourceQuery: `
				SELECT CONCAT(tool_name,'|',status,'|',COUNT(*))
				FROM tool_executions
				GROUP BY tool_name,status`,
			targetQuery: `
				SELECT tool_name || '|' || status || '|' || COUNT(*)::text
				FROM app.tool_executions
				GROUP BY tool_name,status`,
		},
		{
			name: "generated file states",
			sourceQuery: `
				SELECT CONCAT(status,'|',COUNT(*),'|',SUM(size_bytes))
				FROM generated_files
				GROUP BY status`,
			targetQuery: `
				SELECT status || '|' || COUNT(*)::text || '|' || SUM(size_bytes)::text
				FROM app.generated_files
				GROUP BY status`,
		},
	}
	for _, check := range checks {
		sourceValues, err := readKeys(ctx, source, check.sourceQuery)
		if err != nil {
			return fmt.Errorf("read mysql %s: %w", check.name, err)
		}
		targetValues, err := readKeys(ctx, target, check.targetQuery)
		if err != nil {
			return fmt.Errorf("read postgres %s: %w", check.name, err)
		}
		missing, extra := compareKeys(sourceValues, targetValues)
		fmt.Printf(
			"validated %s source=%d target=%d missing=%d extra=%d\n",
			check.name, len(sourceValues), len(targetValues), len(missing), len(extra),
		)
		if len(missing) > 0 || len(extra) > 0 {
			return fmt.Errorf(
				"%s aggregate mismatch: missing=%s extra=%s",
				check.name, preview(missing), preview(extra),
			)
		}
	}
	return nil
}

func validateGovernanceAggregates(ctx context.Context, source, target *sql.DB) error {
	checks := []struct {
		name        string
		sourceQuery string
		targetQuery string
	}{
		{
			name: "workspace member states",
			sourceQuery: `
				SELECT CONCAT(role,'|',status,'|',COUNT(*))
				FROM workspace_members
				GROUP BY role,status`,
			targetQuery: `
				SELECT role || '|' || status || '|' || COUNT(*)::text
				FROM app.workspace_members
				GROUP BY role,status`,
		},
		{
			name: "email delivery states",
			sourceQuery: `
				SELECT CONCAT(status,'|',COUNT(*),'|',SUM(attempts))
				FROM email_deliveries
				GROUP BY status`,
			targetQuery: `
				SELECT status || '|' || COUNT(*)::text || '|' || SUM(attempts)::text
				FROM app.email_deliveries
				GROUP BY status`,
		},
		{
			name: "billing subscription states",
			sourceQuery: `
				SELECT CONCAT(plan_code,'|',status,'|',COUNT(*))
				FROM billing_subscriptions
				GROUP BY plan_code,status`,
			targetQuery: `
				SELECT plan_code || '|' || status || '|' || COUNT(*)::text
				FROM app.billing_subscriptions
				GROUP BY plan_code,status`,
		},
		{
			name: "safety policy states",
			sourceQuery: `
				SELECT CONCAT(minor_mode,'|',risky_skills_allowed,'|',COUNT(*))
				FROM user_safety_policies
				GROUP BY minor_mode,risky_skills_allowed`,
			targetQuery: `
				SELECT minor_mode::int::text || '|' || risky_skills_allowed::int::text || '|' ||
					COUNT(*)::text
				FROM app.user_safety_policies
				GROUP BY minor_mode,risky_skills_allowed`,
		},
		{
			name: "operator account states",
			sourceQuery: `
				SELECT CONCAT(role,'|',status,'|',mfa_enabled,'|',COUNT(*))
				FROM operator_accounts
				GROUP BY role,status,mfa_enabled`,
			targetQuery: `
				SELECT role || '|' || status || '|' || mfa_enabled::int::text || '|' ||
					COUNT(*)::text
				FROM app.operator_accounts
				GROUP BY role,status,mfa_enabled`,
		},
		{
			name: "audit action states",
			sourceQuery: `
				SELECT CONCAT(actor_type,'|',action,'|',COUNT(*))
				FROM audit_logs
				GROUP BY actor_type,action`,
			targetQuery: `
				SELECT actor_type || '|' || action || '|' || COUNT(*)::text
				FROM eventing.audit_logs
				GROUP BY actor_type,action`,
		},
	}
	for _, check := range checks {
		sourceValues, err := readKeys(ctx, source, check.sourceQuery)
		if err != nil {
			return fmt.Errorf("read mysql %s: %w", check.name, err)
		}
		targetValues, err := readKeys(ctx, target, check.targetQuery)
		if err != nil {
			return fmt.Errorf("read postgres %s: %w", check.name, err)
		}
		missing, extra := compareKeys(sourceValues, targetValues)
		fmt.Printf(
			"validated %s source=%d target=%d missing=%d extra=%d\n",
			check.name, len(sourceValues), len(targetValues), len(missing), len(extra),
		)
		if len(missing) > 0 || len(extra) > 0 {
			return fmt.Errorf(
				"%s aggregate mismatch: missing=%s extra=%s",
				check.name, preview(missing), preview(extra),
			)
		}
	}
	return validateWorkspaceShareConstraints(ctx, target)
}

func validateWorkspaceShareConstraints(ctx context.Context, target *sql.DB) error {
	rows, err := target.QueryContext(ctx, `
		SELECT conname
		FROM pg_constraint
		WHERE conname IN (
			'workspace_ledger_export_shares_workspace_fk',
			'workspace_document_shares_workspace_fk',
			'workspace_generated_file_shares_workspace_fk'
		)
		AND NOT convalidated
		ORDER BY conname`)
	if err != nil {
		return err
	}
	defer rows.Close()
	invalid := make([]string, 0)
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return err
		}
		invalid = append(invalid, name)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(invalid) > 0 {
		return fmt.Errorf("workspace share constraints are not validated: %s", strings.Join(invalid, ","))
	}
	fmt.Printf("validated 3 workspace share constraints\n")
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readKeys(ctx context.Context, db queryer, query string) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key any
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		if bytes, ok := key.([]byte); ok {
			key = string(bytes)
		}
		keys = append(keys, fmt.Sprint(key))
	}
	sort.Strings(keys)
	return keys, rows.Err()
}

func compareKeys(source, target []string) ([]string, []string) {
	sourceSet := make(map[string]struct{}, len(source))
	targetSet := make(map[string]struct{}, len(target))
	for _, key := range source {
		sourceSet[key] = struct{}{}
	}
	for _, key := range target {
		targetSet[key] = struct{}{}
	}
	missing := make([]string, 0)
	extra := make([]string, 0)
	for key := range sourceSet {
		if _, ok := targetSet[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range targetSet {
		if _, ok := sourceSet[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func preview(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	if len(values) > 5 {
		values = values[:5]
	}
	return "[" + strings.Join(values, ",") + "]"
}

func coreTables() []tableSpec {
	return []tableSpec{
		{
			name: "users", target: "app.users",
			columns:    fields("id,email,password_hash,display_name,timezone,locale,status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),email,password_hash,display_name,timezone,locale,status,created_at,updated_at FROM users ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM users`,
			targetKeys: `SELECT id::text FROM app.users`,
		},
		{
			name: "user_devices", target: "app.user_devices",
			columns:    fields("id,user_id,device_key,name,platform,timezone,last_seen_at,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),device_key,name,platform,timezone,last_seen_at,created_at FROM user_devices ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM user_devices`,
			targetKeys: `SELECT id::text FROM app.user_devices`,
		},
		{
			name: "refresh_sessions", target: "app.refresh_sessions",
			columns:    fields("id,user_id,device_id,token_hash,expires_at,last_used_at,revoked_at,rotated_to_id,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(device_id),token_hash,expires_at,last_used_at,revoked_at,NULL,created_at FROM refresh_sessions ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM refresh_sessions`,
			targetKeys: `SELECT id::text FROM app.refresh_sessions`,
		},
		{
			name: "characters", target: "app.characters",
			columns:    fields("id,user_id,module_key,name,avatar_url,relationship_label,personality,speech_style,hobbies,boundaries,initiative,reply_length,sticker_style,raw_prompt,persona_version,status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),module_key,name,avatar_url,relationship_label,personality,speech_style,hobbies,boundaries,initiative,reply_length,sticker_style,raw_prompt,persona_version,status,created_at,updated_at FROM characters ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM characters`,
			targetKeys: `SELECT id::text FROM app.characters`,
		},
		{
			name: "character_persona_versions", target: "app.character_persona_versions",
			columns:    fields("character_id,version,compiler_version,compiled_persona,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(character_id),version,compiler_version,compiled_persona,created_at FROM character_persona_versions ORDER BY character_id,version`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(character_id),':',version) FROM character_persona_versions`,
			targetKeys: `SELECT character_id::text || ':' || version::text FROM app.character_persona_versions`,
		},
		{
			name: "conversations", target: "app.conversations",
			columns:    fields("id,user_id,character_id,title,status,next_sequence,last_message_at,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(character_id),title,status,next_sequence,last_message_at,created_at,updated_at FROM conversations ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM conversations`,
			targetKeys: `SELECT id::text FROM app.conversations`,
		},
		{
			name: "messages", target: "app.messages",
			columns:    fields("id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,reply_to_id,created_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(conversation_id),BIN_TO_UUID(user_id),role,sequence_no,bubble_no,content,status,NULL,created_at,completed_at FROM messages ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM messages`,
			targetKeys: `SELECT id::text FROM app.messages`,
		},
		{
			name: "long_term_memories", target: "app.long_term_memories",
			columns:    fields("id,user_id,memory_type,content,normalized_hash,source_conversation_id,source_message_id,confidence,importance,sensitivity,pinned,status,valid_from,valid_to,supersedes_id,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),memory_type,content,normalized_hash,BIN_TO_UUID(source_conversation_id),BIN_TO_UUID(source_message_id),confidence,importance,sensitivity,pinned,status,valid_from,valid_to,NULL,created_at,updated_at FROM long_term_memories ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM long_term_memories`,
			targetKeys: `SELECT id::text FROM app.long_term_memories`,
			normalize:  normalizeBooleans(10),
		},
		{
			name: "generation_jobs", target: "app.generation_jobs",
			columns:    fields("id,conversation_id,user_message_id,status,attempt,model_provider,model_name,deadline_at,available_at,worker_id,lease_expires_at,error_code,error_message,created_at,started_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(conversation_id),BIN_TO_UUID(user_message_id),status,attempt,model_provider,model_name,deadline_at,available_at,worker_id,lease_expires_at,error_code,error_message,created_at,started_at,completed_at FROM generation_jobs ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM generation_jobs`,
			targetKeys: `SELECT id::text FROM app.generation_jobs`,
		},
		{
			name: "model_usage_records", target: "app.model_usage_records",
			columns:    fields("id,user_id,generation_job_id,provider,model,input_tokens,output_tokens,estimated_cost_micros,latency_ms,created_at"),
			sourceRows: `SELECT id,BIN_TO_UUID(user_id),BIN_TO_UUID(generation_job_id),provider,model,input_tokens,output_tokens,estimated_cost_micros,latency_ms,created_at FROM model_usage_records ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM model_usage_records`,
			targetKeys: `SELECT id::text FROM app.model_usage_records`,
		},
		{
			name: "generation_job_events", target: "app.generation_job_events",
			columns:    fields("id,generation_job_id,event_type,event_data,created_at"),
			sourceRows: `SELECT id,BIN_TO_UUID(generation_job_id),event_type,event_data,created_at FROM generation_job_events ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM generation_job_events`,
			targetKeys: `SELECT id::text FROM app.generation_job_events`,
		},
		{
			name: "conversation_summaries", target: "app.conversation_summaries",
			columns:    fields("id,conversation_id,user_id,version,start_sequence,end_sequence,range_started_at,range_ended_at,content,token_count,summarizer_version,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(conversation_id),BIN_TO_UUID(user_id),version,start_sequence,end_sequence,range_started_at,range_ended_at,content,token_count,summarizer_version,created_at FROM conversation_summaries ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM conversation_summaries`,
			targetKeys: `SELECT id::text FROM app.conversation_summaries`,
		},
		{
			name: "outbox_events", target: "eventing.outbox_events",
			columns:    fields("id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,published_at,status,available_at,attempts,last_error,worker_id,lease_expires_at,published_topic,published_partition,published_offset"),
			sourceRows: `SELECT BIN_TO_UUID(id),aggregate_type,BIN_TO_UUID(aggregate_id),event_type,event_version,payload,occurred_at,published_at,status,available_at,attempts,last_error,worker_id,lease_expires_at,published_topic,published_partition,published_offset FROM outbox_events ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM outbox_events`,
			targetKeys: `SELECT id::text FROM eventing.outbox_events`,
		},
	}
}

func lifeTables() []tableSpec {
	return []tableSpec{
		{
			name: "ledger_candidates", target: "app.ledger_candidates",
			columns:    fields("id,user_id,source_message_id,raw_text,direction,currency,amount_minor,category,merchant,occurred_at,timezone,time_precision,confidence,needs_clarification,status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(source_message_id),raw_text,direction,currency,amount_minor,category,merchant,occurred_at,timezone,time_precision,confidence,needs_clarification,status,created_at,updated_at FROM ledger_candidates ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM ledger_candidates`,
			targetKeys: `SELECT id::text FROM app.ledger_candidates`,
		},
		{
			name: "ledger_entries", target: "app.ledger_entries",
			columns:    fields("id,user_id,candidate_id,idempotency_key,direction,currency,amount_minor,category,merchant,occurred_at,timezone,note,status,created_at,updated_at,deleted_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(candidate_id),idempotency_key,direction,currency,amount_minor,category,merchant,occurred_at,timezone,note,status,created_at,updated_at,deleted_at FROM ledger_entries ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM ledger_entries`,
			targetKeys: `SELECT id::text FROM app.ledger_entries`,
		},
		{
			name: "ledger_exports", target: "app.ledger_exports",
			columns:    fields("id,user_id,month,currency,timezone,request_key,status,available_at,attempts,worker_id,lease_expires_at,storage_key,file_name,media_type,sha256,size_bytes,failure_code,last_error,created_at,updated_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),month,currency,timezone,request_key,status,available_at,attempts,worker_id,lease_expires_at,storage_key,file_name,media_type,sha256,size_bytes,failure_code,last_error,created_at,updated_at,completed_at FROM ledger_exports ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM ledger_exports`,
			targetKeys: `SELECT id::text FROM app.ledger_exports`,
		},
		{
			name: "plans", target: "app.plans",
			columns:    fields("id,user_id,title,local_date,timezone,status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),title,local_date,timezone,status,created_at,updated_at FROM plans ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM plans`,
			targetKeys: `SELECT id::text FROM app.plans`,
		},
		{
			name: "plan_items", target: "app.plan_items",
			columns:    fields("id,plan_id,title,priority,estimated_minutes,starts_at,ends_at,location,status,source,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(plan_id),title,priority,estimated_minutes,starts_at,ends_at,location,status,source,created_at,updated_at FROM plan_items ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM plan_items`,
			targetKeys: `SELECT id::text FROM app.plan_items`,
		},
		{
			name: "reminders", target: "app.reminders",
			columns:    fields("id,user_id,plan_item_id,source_message_id,raw_text,title,due_at,local_due,timezone,time_precision,recurrence,needs_clarification,status,confirmation_key,system_sync_status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(plan_item_id),BIN_TO_UUID(source_message_id),raw_text,title,due_at,local_due,timezone,time_precision,recurrence,needs_clarification,status,confirmation_key,system_sync_status,created_at,updated_at FROM reminders ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM reminders`,
			targetKeys: `SELECT id::text FROM app.reminders`,
		},
		{
			name: "external_reminder_links", target: "app.external_reminder_links",
			columns:    fields("reminder_id,provider,external_id,external_revision,sync_status,last_error_code,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(reminder_id),provider,external_id,external_revision,sync_status,last_error_code,created_at,updated_at FROM external_reminder_links ORDER BY reminder_id`,
			sourceKeys: `SELECT BIN_TO_UUID(reminder_id) FROM external_reminder_links`,
			targetKeys: `SELECT reminder_id::text FROM app.external_reminder_links`,
		},
		{
			name: "reminder_events", target: "app.reminder_events",
			columns:    fields("id,reminder_id,user_id,event_type,event_data,occurred_at"),
			sourceRows: `SELECT id,BIN_TO_UUID(reminder_id),BIN_TO_UUID(user_id),event_type,event_data,occurred_at FROM reminder_events ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM reminder_events`,
			targetKeys: `SELECT id::text FROM app.reminder_events`,
		},
		{
			name: "notification_deliveries", target: "app.notification_deliveries",
			columns:    fields("id,reminder_id,user_id,channel,scheduled_at,status,enqueued_at,provider_message_id,failure_code,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(reminder_id),BIN_TO_UUID(user_id),channel,scheduled_at,status,enqueued_at,provider_message_id,failure_code,created_at,updated_at FROM notification_deliveries ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM notification_deliveries`,
			targetKeys: `SELECT id::text FROM app.notification_deliveries`,
		},
		{
			name: "workspace_ledger_export_shares", target: "app.workspace_ledger_export_shares",
			columns:    fields("workspace_id,export_id,shared_by,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(workspace_id),BIN_TO_UUID(export_id),BIN_TO_UUID(shared_by),created_at FROM workspace_ledger_export_shares ORDER BY workspace_id,export_id`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(workspace_id),':',BIN_TO_UUID(export_id)) FROM workspace_ledger_export_shares`,
			targetKeys: `SELECT workspace_id::text || ':' || export_id::text FROM app.workspace_ledger_export_shares`,
		},
	}
}

func toolTables() []tableSpec {
	return []tableSpec{
		{
			name: "files", target: "app.files",
			columns:    fields("id,user_id,original_name,media_type,size_bytes,sha256,storage_key,status,created_at,updated_at,deleted_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),original_name,media_type,size_bytes,sha256,storage_key,status,created_at,updated_at,deleted_at FROM files ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM files`,
			targetKeys: `SELECT id::text FROM app.files`,
		},
		{
			name: "documents", target: "app.documents",
			columns:    fields("id,user_id,file_id,ingest_status,parser_version,page_count,chunk_count,failure_code,created_at,updated_at,deleted_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(file_id),ingest_status,parser_version,page_count,chunk_count,failure_code,created_at,updated_at,deleted_at FROM documents ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM documents`,
			targetKeys: `SELECT id::text FROM app.documents`,
		},
		{
			name: "document_ingest_jobs", target: "app.document_ingest_jobs",
			columns:    fields("id,document_id,user_id,status,idempotency_key,attempts,available_at,started_at,finished_at,last_error,worker_id,lease_expires_at,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(document_id),BIN_TO_UUID(user_id),status,idempotency_key,attempts,available_at,started_at,finished_at,last_error,worker_id,lease_expires_at,created_at,updated_at FROM document_ingest_jobs ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM document_ingest_jobs`,
			targetKeys: `SELECT id::text FROM app.document_ingest_jobs`,
		},
		{
			name: "document_pages", target: "app.document_pages",
			columns:    fields("id,document_id,user_id,page_no,text_content,quality_score,parser_version,content_hash,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(document_id),BIN_TO_UUID(user_id),page_no,text_content,quality_score,parser_version,content_hash,created_at FROM document_pages ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM document_pages`,
			targetKeys: `SELECT id::text FROM app.document_pages`,
		},
		{
			name: "document_chunks", target: "app.document_chunks",
			columns:    fields("id,point_id,document_id,user_id,ordinal_no,page_start,page_end,section_path,content,token_count,content_hash,parser_version,embedding_version,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(point_id),BIN_TO_UUID(document_id),BIN_TO_UUID(user_id),ordinal_no,page_start,page_end,section_path,content,token_count,content_hash,parser_version,embedding_version,created_at FROM document_chunks ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM document_chunks`,
			targetKeys: `SELECT id::text FROM app.document_chunks`,
		},
		{
			name: "document_cleanup_jobs", target: "app.document_cleanup_jobs",
			columns:    fields("id,document_id,user_id,storage_key,status,attempts,available_at,worker_id,lease_expires_at,last_error,created_at,updated_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(document_id),BIN_TO_UUID(user_id),storage_key,status,attempts,available_at,worker_id,lease_expires_at,last_error,created_at,updated_at,completed_at FROM document_cleanup_jobs ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM document_cleanup_jobs`,
			targetKeys: `SELECT id::text FROM app.document_cleanup_jobs`,
		},
		{
			name: "workspace_document_shares", target: "app.workspace_document_shares",
			columns:    fields("workspace_id,document_id,shared_by,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(workspace_id),BIN_TO_UUID(document_id),BIN_TO_UUID(shared_by),created_at FROM workspace_document_shares ORDER BY workspace_id,document_id`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(workspace_id),':',BIN_TO_UUID(document_id)) FROM workspace_document_shares`,
			targetKeys: `SELECT workspace_id::text || ':' || document_id::text FROM app.workspace_document_shares`,
		},
		{
			name: "skill_runs", target: "app.skill_runs",
			columns:    fields("id,user_id,skill_name,skill_version,execution_mode,status,current_state,risk_level,requires_confirmation,input_json,output_json,create_key,confirmation_key,last_action,last_action_key,attempt,max_steps,timeout_ms,max_input_bytes,max_cost_micros,error_code,error_message,revision,available_at,worker_id,lease_expires_at,created_at,updated_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),skill_name,skill_version,execution_mode,status,current_state,risk_level,requires_confirmation,input_json,output_json,create_key,confirmation_key,last_action,last_action_key,attempt,max_steps,timeout_ms,max_input_bytes,max_cost_micros,error_code,error_message,revision,available_at,worker_id,lease_expires_at,created_at,updated_at,completed_at FROM skill_runs ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM skill_runs`,
			targetKeys: `SELECT id::text FROM app.skill_runs`,
			normalize:  normalizeBooleans(8),
		},
		{
			name: "skill_run_action_keys", target: "app.skill_run_action_keys",
			columns:    fields("id,run_id,user_id,action,idempotency_key,created_at"),
			sourceRows: `SELECT id,BIN_TO_UUID(run_id),BIN_TO_UUID(user_id),action,idempotency_key,created_at FROM skill_run_action_keys ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM skill_run_action_keys`,
			targetKeys: `SELECT id::text FROM app.skill_run_action_keys`,
		},
		{
			name: "skill_run_steps", target: "app.skill_run_steps",
			columns:    fields("id,run_id,sequence_no,state,status,tool_name,input_json,output_json,error_code,error_message,started_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(run_id),sequence_no,state,status,tool_name,input_json,output_json,error_code,error_message,started_at,completed_at FROM skill_run_steps ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM skill_run_steps`,
			targetKeys: `SELECT id::text FROM app.skill_run_steps`,
		},
		{
			name: "tool_executions", target: "app.tool_executions",
			columns:    fields("id,run_id,user_id,tool_name,risk_level,status,idempotency_key,input_sha256,output_json,error_code,error_message,started_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(run_id),BIN_TO_UUID(user_id),tool_name,risk_level,status,idempotency_key,input_sha256,output_json,error_code,error_message,started_at,completed_at FROM tool_executions ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM tool_executions`,
			targetKeys: `SELECT id::text FROM app.tool_executions`,
		},
		{
			name: "generated_files", target: "app.generated_files",
			columns:    fields("id,run_id,user_id,display_name,media_type,size_bytes,sha256,storage_key,status,created_at,deleted_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(run_id),BIN_TO_UUID(user_id),display_name,media_type,size_bytes,sha256,storage_key,status,created_at,deleted_at FROM generated_files ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM generated_files`,
			targetKeys: `SELECT id::text FROM app.generated_files`,
		},
		{
			name: "workspace_generated_file_shares", target: "app.workspace_generated_file_shares",
			columns:    fields("workspace_id,file_id,shared_by,created_at"),
			sourceRows: `SELECT BIN_TO_UUID(workspace_id),BIN_TO_UUID(file_id),BIN_TO_UUID(shared_by),created_at FROM workspace_generated_file_shares ORDER BY workspace_id,file_id`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(workspace_id),':',BIN_TO_UUID(file_id)) FROM workspace_generated_file_shares`,
			targetKeys: `SELECT workspace_id::text || ':' || file_id::text FROM app.workspace_generated_file_shares`,
		},
		{
			name: "user_skill_settings", target: "app.user_skill_settings",
			columns:    fields("user_id,skill_name,enabled,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(user_id),skill_name,enabled,created_at,updated_at FROM user_skill_settings ORDER BY user_id,skill_name`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(user_id),':',skill_name) FROM user_skill_settings`,
			targetKeys: `SELECT user_id::text || ':' || skill_name FROM app.user_skill_settings`,
			normalize:  normalizeBooleans(2),
		},
	}
}

func governanceTables() []tableSpec {
	return []tableSpec{
		{
			name: "billing_plans", target: "app.billing_plans",
			columns:    fields("code,display_name,status,document_limit,skill_runs_monthly_limit,workspace_limit,created_at,updated_at"),
			sourceRows: `SELECT code,display_name,status,document_limit,skill_runs_monthly_limit,workspace_limit,created_at,updated_at FROM billing_plans ORDER BY code`,
			sourceKeys: `SELECT code FROM billing_plans`,
			targetKeys: `SELECT code FROM app.billing_plans`,
		},
		{
			name: "workspaces", target: "app.workspaces",
			columns:    fields("id,name,owner_id,status,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),name,BIN_TO_UUID(owner_id),status,created_at,updated_at FROM workspaces ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM workspaces`,
			targetKeys: `SELECT id::text FROM app.workspaces`,
		},
		{
			name: "workspace_members", target: "app.workspace_members",
			columns:    fields("workspace_id,user_id,role,status,joined_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(workspace_id),BIN_TO_UUID(user_id),role,status,joined_at,updated_at FROM workspace_members ORDER BY workspace_id,user_id`,
			sourceKeys: `SELECT CONCAT(BIN_TO_UUID(workspace_id),':',BIN_TO_UUID(user_id)) FROM workspace_members`,
			targetKeys: `SELECT workspace_id::text || ':' || user_id::text FROM app.workspace_members`,
		},
		{
			name: "workspace_invitations", target: "app.workspace_invitations",
			columns:    fields("id,workspace_id,email,role,status,invited_by,created_at,expires_at,accepted_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(workspace_id),email,role,status,BIN_TO_UUID(invited_by),created_at,expires_at,accepted_at FROM workspace_invitations ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM workspace_invitations`,
			targetKeys: `SELECT id::text FROM app.workspace_invitations`,
		},
		{
			name: "email_deliveries", target: "app.email_deliveries",
			columns:    fields("id,actor_id,resource_type,resource_id,template,recipient_email,subject,body_text,status,provider,provider_message_id,failure_code,last_error,attempts,available_at,worker_id,lease_expires_at,created_at,updated_at,sent_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(actor_id),resource_type,BIN_TO_UUID(resource_id),template,recipient_email,subject,body_text,status,provider,provider_message_id,failure_code,last_error,attempts,available_at,worker_id,lease_expires_at,created_at,updated_at,sent_at FROM email_deliveries ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM email_deliveries`,
			targetKeys: `SELECT id::text FROM app.email_deliveries`,
		},
		{
			name: "billing_subscriptions", target: "app.billing_subscriptions",
			columns:    fields("id,user_id,plan_code,status,current_period_start,current_period_end,provider,provider_ref,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),plan_code,status,current_period_start,current_period_end,provider,provider_ref,created_at,updated_at FROM billing_subscriptions ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM billing_subscriptions`,
			targetKeys: `SELECT id::text FROM app.billing_subscriptions`,
		},
		{
			name: "user_safety_policies", target: "app.user_safety_policies",
			columns:    fields("user_id,minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at"),
			sourceRows: `SELECT BIN_TO_UUID(user_id),minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at FROM user_safety_policies ORDER BY user_id`,
			sourceKeys: `SELECT BIN_TO_UUID(user_id) FROM user_safety_policies`,
			targetKeys: `SELECT user_id::text FROM app.user_safety_policies`,
			normalize:  normalizeBooleans(1, 3),
		},
		{
			name: "operator_accounts", target: "app.operator_accounts",
			columns:    fields("id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at,last_authenticated_at"),
			sourceRows: `SELECT id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at,last_authenticated_at FROM operator_accounts ORDER BY id`,
			sourceKeys: `SELECT id FROM operator_accounts`,
			targetKeys: `SELECT id FROM app.operator_accounts`,
			normalize:  normalizeBooleans(6),
		},
		{
			name: "inbox_events", target: "eventing.inbox_events",
			columns:    fields("consumer_name,event_id,processed_at"),
			sourceRows: `SELECT consumer_name,BIN_TO_UUID(event_id),processed_at FROM inbox_events ORDER BY consumer_name,event_id`,
			sourceKeys: `SELECT CONCAT(consumer_name,':',BIN_TO_UUID(event_id)) FROM inbox_events`,
			targetKeys: `SELECT consumer_name || ':' || event_id::text FROM eventing.inbox_events`,
		},
		{
			name: "kafka_poison_messages", target: "eventing.kafka_poison_messages",
			columns:    fields("id,consumer_name,topic,partition_no,offset_no,event_id,event_type,aggregate_id,reason,envelope,observed_at"),
			sourceRows: `SELECT id,consumer_name,topic,partition_no,offset_no,event_id,event_type,aggregate_id,reason,envelope,observed_at FROM kafka_poison_messages ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM kafka_poison_messages`,
			targetKeys: `SELECT id::text FROM eventing.kafka_poison_messages`,
		},
		{
			name: "audit_logs", target: "eventing.audit_logs",
			columns:    fields("id,actor_type,actor_id,action,resource_type,resource_id,trace_id,metadata,occurred_at"),
			sourceRows: `SELECT id,actor_type,BIN_TO_UUID(actor_id),action,resource_type,BIN_TO_UUID(resource_id),trace_id,metadata,occurred_at FROM audit_logs ORDER BY id`,
			sourceKeys: `SELECT CAST(id AS CHAR) FROM audit_logs`,
			targetKeys: `SELECT id::text FROM eventing.audit_logs`,
		},
		{
			name: "compensation_records", target: "eventing.compensation_records",
			columns:    fields("id,source_type,source_id,action,reason,actor,status,metadata,created_at,completed_at"),
			sourceRows: `SELECT BIN_TO_UUID(id),source_type,source_id,action,reason,actor,status,metadata,created_at,completed_at FROM compensation_records ORDER BY id`,
			sourceKeys: `SELECT BIN_TO_UUID(id) FROM compensation_records`,
			targetKeys: `SELECT id::text FROM eventing.compensation_records`,
		},
	}
}

func sequenceResetStatements(specs []tableSpec) []string {
	names := make(map[string]bool, len(specs))
	for _, spec := range specs {
		names[spec.name] = true
	}
	statements := make([]string, 0, 3)
	if names["model_usage_records"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('app.model_usage_records','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM app.model_usage_records`)
	}
	if names["generation_job_events"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('app.generation_job_events','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM app.generation_job_events`)
	}
	if names["reminder_events"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('app.reminder_events','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM app.reminder_events`)
	}
	if names["skill_run_action_keys"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('app.skill_run_action_keys','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM app.skill_run_action_keys`)
	}
	if names["kafka_poison_messages"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('eventing.kafka_poison_messages','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM eventing.kafka_poison_messages`)
	}
	if names["audit_logs"] {
		statements = append(statements, `SELECT setval(pg_get_serial_sequence('eventing.audit_logs','id'), COALESCE(MAX(id),1), MAX(id) IS NOT NULL) FROM eventing.audit_logs`)
	}
	return statements
}

func finalizationStatements(specs []tableSpec) []string {
	for _, spec := range specs {
		if spec.name != "workspaces" {
			continue
		}
		return []string{
			`ALTER TABLE app.workspace_ledger_export_shares VALIDATE CONSTRAINT workspace_ledger_export_shares_workspace_fk`,
			`ALTER TABLE app.workspace_document_shares VALIDATE CONSTRAINT workspace_document_shares_workspace_fk`,
			`ALTER TABLE app.workspace_generated_file_shares VALIDATE CONSTRAINT workspace_generated_file_shares_workspace_fk`,
		}
	}
	return nil
}

func normalizeBooleans(indices ...int) func([]any) error {
	return func(values []any) error {
		for _, index := range indices {
			if index < 0 || index >= len(values) {
				return fmt.Errorf("boolean column index %d is out of range", index)
			}
			switch value := values[index].(type) {
			case bool:
			case int64:
				values[index] = value != 0
			case uint64:
				values[index] = value != 0
			case []byte:
				values[index] = string(value) != "0"
			case string:
				values[index] = value != "0" && !strings.EqualFold(value, "false")
			default:
				return fmt.Errorf("unsupported boolean value %T", value)
			}
		}
		return nil
	}
}

func fields(value string) []string {
	return strings.Split(value, ",")
}
