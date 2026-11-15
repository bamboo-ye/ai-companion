# 健康助手

健康助手来自 `healthy_care` 的个人健康空间。登录伴 AI 后从侧栏进入，无需另建账号。使用图文对话记录健康变化，核对草稿后写入病程、实际用药或随访提醒。在「我的与隐私」可直接导出或清除记录。健康助手用于日常记录和护理参考，不提供诊断、处方或在线就诊。

## 启用

在仓库根目录运行：

```sh
make health-init
```

脚本只在私有 `.env` 中补充缺少的随机密码及服务令牌。要开启对话，在同一个文件中配置支持图片、流式返回和 JSON 输出的兼容模型：

```dotenv
HEALTH_AGENT_PROVIDER=openai_compatible
HEALTH_MODEL_BASE_URL=https://your-model-provider.example/v1
HEALTH_MODEL_NAME=your-vision-model
HEALTH_MODEL_API_KEY=your-model-key
```

然后运行 `make docker-up-health`，打开 [伴 AI](http://localhost:3000) 并登录。普通 `make docker-up` 无需配置健康服务；点击健康助手会显示未启用提示。健康服务已启用但模型未配置时，仍可查看、导出和清除已有健康数据。开发联调可以使用 `HEALTH_AGENT_PROVIDER=test` 和 `HEALTH_ENABLE_TEST_PROVIDER=true`；测试输出只用于合成验收。

已有生产 Compose 启动命令应追加 `-f deploy/compose/compose.health.yml`，并在同一部署的私有环境配置这些变量。健康服务不映射宿主端口；数据库、附件和备份使用独立卷。查看状态或停止健康部署时使用同一组 Compose 文件和 profiles，例如：

```sh
docker compose --profile app --profile agent --profile agent-runtime --profile observability --env-file .env \
  -f deploy/compose/compose.yml -f deploy/compose/compose.health.yml ps
```

## 数据与维护

- 主 API 验证当前伴 AI 会话后，用服务令牌转发健康请求。健康服务通过账户 ID 区分记录；不会复制伴 AI 邮箱、密码、通用会话或长期记忆。每个用户在首次进入时创建一个空健康空间。
- 健康模型会接收生成当次回复需要的文字、照片和相关健康记录。配置外部模型前应确认服务提供方如何处理这些数据；健康模型凭据独立于伴 AI 通用模型。
- 清除记录会移除当前健康空间的数据并安排后台清理图片和模型检查点。备份由健康服务独立维护，`HEALTH_BACKUP_KEEP_DAYS` 默认是 7 天；已导出的副本由下载者管理。
- 本集成未开放科研工作台、研究授权或 SciencePal。原项目已有账号、记录、附件及数据库不自动迁移。
- 隔离的健康 API 单元测试可用 `make test-health` 运行。健康服务来源和改动见 [服务说明](../services/healthy_care/README.md)。
