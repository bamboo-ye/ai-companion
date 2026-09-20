# 管理用户额度

在 `/admin` 打开「配置中心 → 用户额度与用量」。管理员完成通行密钥验证后可设置全体用户或指定用户的额度；支持人员只能查看。

- **全体用户额度**：作用于全部现有用户和未来注册用户，包括付费套餐。保存前需确认适用范围。
- **指定用户额度**：通过邮箱、昵称或用户 ID 搜索用户，对各项资源单独设置。
- 每项资源独立按「用户单独设置 → 全体用户设置 → 用户套餐」确定有效上限。选择继承或「全部恢复继承」并保存，可恢复上一级设置。
- `0` 阻止新增使用，`-1` 表示不限额。前端通过下拉选项设置不限额，金额以美元输入，最多六位小数。
- 可管理活跃文档数、工作区数、每月 Skill 次数、每月 Agent 次数和每月模型成本。月度资源沿用原有订阅周期，无订阅时为 UTC 自然月。
- 修改上限不清空实际用量，不删除已有资源，也不取消正在执行的任务。后续请求立即读取有效上限；现有额度检查按请求开始时已累计的用量拦截，不提供运行中逐 Token 硬预算或并发额度预留。
- 「已用量纠正」仍用于补偿或纠错，与修改上限分开记录。

所有修改必须填写原因，持久数据库在同一事务中保存策略和 `billing.quota.update` 审计（操作者、作用范围、修改前后值及版本）。并发修改返回 409，重新加载后再保存，不自动覆盖其他管理员的修改。全空策略仍保留版本，防止旧页面覆盖恢复继承后的新设置。

## 部署

先执行 PostgreSQL `000043_billing_quota_policies` 或 MySQL `000032_billing_quota_policies`，再更新 API 和 Web。迁移只新增策略表，默认没有覆盖设置，所有用户继续使用现有套餐额度。没有为任何真实用户预设新的限额。

API：`GET/PUT /v1/ops/billing/quotas`、`GET/PUT /v1/ops/billing/users/{user_id}/quotas`。PUT 替换该范围的完整设置，必须提交 `limits`、当前 `revision`（初始为 0）、`reason`；空或省略的资源字段表示继承。浏览器写请求要求同源 CSRF 校验与最近五分钟内的 Passkey 验证。

```json
{
  "limits": {
    "documents": 100,
    "skill_runs_per_month": null,
    "workspaces": null,
    "agent_runs_per_month": -1,
    "model_cost_micros_monthly": 10000000
  },
  "revision": 0,
  "reason": "根据账户服务约定调整额度"
}
```

`GET /v1/billing/me` 和后台用量响应新增 `effective_limits`；各用量项的 `limit_source` 标注 `plan`、`global`、`user` 或开发环境豁免 `development`。`plan.limits` 保留套餐原始定义。

## 验证

```sh
go test -race ./internal/billing ./internal/httpserver
cd web && pnpm check && pnpm test && pnpm build
```

在应用全部迁移的可丢弃数据库中设置 `BILLING_QUOTA_POSTGRES_TEST_DSN` / `BILLING_QUOTA_MYSQL_TEST_DSN`，运行 `go test ./internal/billing -run TestDurableQuotaPolicies -v` 可验证持久化、并发版本保护和审计失败时的事务回滚。不得指向生产数据库。
