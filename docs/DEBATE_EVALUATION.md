# 辩论对照评测格式

每行是一组完整 Run 的人工标注与真实观测。所有任务必须包含四个 arm：`baseline_single`（现有一次仲裁）、`single_supplement`（单 Agent 定向补证）、`repeat_same_evidence`（不增加资料的重复判断对照）、`debate`（有界辩论补证）。同任务的原始资料哈希、模型指纹、测量类别与预算必须相同；最终证据允许因补证策略不同而不同。样例仅解释格式，不是实测数据：

```json
{"case_id":"budget-001","arm":"debate","source_snapshot_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model_fingerprint":"provider/model/prompt/config-fingerprint","limits":{"model_calls":20,"read_calls":5,"cost_micros":10000},"wrong_accept":false,"false_reject":false,"resolved":true,"false_consensus":false,"valid_supplement":true,"latency_ms":18000,"cost_micros":4500,"model_calls":12,"read_calls":3,"scope":"full_run","measurement":"live","usage_complete":true}
```

- `wrong_accept`：采纳了人工核验为错误的事实；`false_reject`：驳回了本应采纳的事实。
- `resolved`：争议得到有充分依据的处理；不能仅根据模型输出 resolved 标注。
- `false_consensus`：参与者一致但结论错误；`valid_supplement`：取得了确实能区分争议的新原文。
- 费用、延迟与调用数涵盖完整 Run（含失败、正文综合、修订、复核）；缺失实际供应商账单时 `usage_complete=false`。零费用必须是真实计费为零。
- 模型输出、程序门禁与人工标签分开记录。数据集选择和人工标注质量由评测负责人审查，程序无法证明标签真实性。
- 合成验收必须使用 `measurement=synthetic`。真实模型评测使用 `live`，并在采样前锁定任务集和门槛。阈值通过不等于已获部署授权；工具不进行发布或更改实际回复。

至少 100 个完整配对样本只是最低样本门槛，不保证置信区间足够窄。当前默认错误采纳非劣性容差为 1 个百分点，使用保守的 Wilson 区间边界比较，因此零错误的小样本也不会自动通过。报告被拒绝时退出码为 2；输入无效时直接失败，不生成伪成功。
