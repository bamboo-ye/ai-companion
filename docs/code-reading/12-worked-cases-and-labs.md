# 12 把整条链路走通：案例、实验与学习验收

[阅读总入口](README.md) · [语法与技术预备课](00-foundations.md)

这一章解决“每个函数都看懂了，放在一起还是不知道发生了什么”。案例使用虚构用户、ID 和输入，展示当前代码可支持的一条路径；模型路由有不确定性，不能保证一句相同提示在每次运行都选择同样工具。下面没有在真实账号执行记账、发邮件、生成收费模型请求或改动部署。

## 1. 完整案例 A：用户批准一笔账目

### 1.1 先固定前置条件和输入

假设用户 U1 已登录，拥有 life 角色 C1 和会话 V1；life 模块 Agent 已配置，持久化存储和 Worker 可用，配额检查通过，没有活跃 Run。输入为“今天午餐花了 32.5 元，帮我记账”。时区为 Asia/Shanghai。

这里不要急着看模型：先检查这些前置条件来自哪里。认证中间件提供 U1；会话记录提供 C1；角色提供 module=life；服务器配置决定 Agent 是否启用；“我在正文写 user_id=U2”不能改变可信身份。

### 1.2 把每一步的数据和完成标准列出来

| 步骤 | 调用/数据 | 这一步已经完成 | 还没有完成 |
|---|---|---|---|
| 1 输入 | 浏览器读取 content 与 document_ids | 取得页面输入 | 服务端未必收到 |
| 2 请求校验 | Handler 检查身份、频率、额度、会话、内容 | 允许本次输入继续 | 没有正式账目 |
| 3 BuildContext | query 用于相关性选择；构建有来源快照 | 为运行准备上下文 | 未保证模型已运行 |
| 4 AcceptChat | 创建消息 M1、运行 R1；捕获 Agent/模型快照 | 服务层构造运行对象 | 尚需数据库提交 |
| 5 接受事务 | Message+Run+Event+Outbox，共同提交 | 具备持久化执行依据 | 还没记账 |
| 6 HTTP 202 | 返回 M1 与 R1 | 浏览器知道要观察谁 | 不能显示“已记账” |
| 7 领取 | Worker 通过数据库条件取得 owner/revision/lease | 获得有期限执行权 | 外部模型结果未知 |
| 8 Python 决策 | Go 输入协议→Graph→模型建议→校验 | 产生合法的工具提案 | 工具提案不是用户批准 |
| 9 Prepare | 网关核验执行权；领域解析出候选 L1 | 金额等字段成为候选 | 不等于正式 Entry |
| 10 等待审批 | 检查点与 Run 保存等待信息 | 可以关闭页面后恢复 | 未批准不提交对应写入 |
| 11 用户批准 | approved=true，提交一次逻辑决策的键 K1 | 审批结果持久化并使任务可继续 | 不保证后续执行不失败 |
| 12 恢复并 Commit | 同一 Run 恢复；网关再次验权、验证确认信息 | 正式账目 E1 按领域规则保存 | Agent 最终回复还未必提交 |
| 13 完成事务 | 校验当前 owner/revision/期限，保存结果与回复 | 运行终态和消息保存 | 页面可能尚未刷新 |
| 14 展示 | 轮询看到 completed，再拉消息 | 页面显示持久化回答 | 应以账本 E1 核实业务结果 |

阅读入口分别在 [第 02 章](02-chat-intake.md)、[第 03 章](03-agent-transactions.md)、[第 04 章](04-agent-approval-recovery.md)、[第 07 章](07-life-tools.md)、[第 11 章](11-python-model-graph.md)。若模型要求补充日期/分类，则先走澄清路径，不能跳过字段不完整的检查强行套入本例。

### 1.3 用变量解释，而不是只背函数名

content 最初是自然语言字符串；candidate.AmountMinor 是整数 3250；candidate.Status 描述待确认或需澄清；approved 是用户决策；Entry 是正式领域结果。它们不是“同一个对象改几个名字”。

M1 是用户消息，R1 是处理该消息的运行，L1 是领域候选，E1 是正式账目。K1 是某次确认的幂等身份，不是密码、会话 ID，也不是所有请求都可共用的万能键。实际各层确认会有自己的键和关联，阅读时按字段传递追踪，不要见到 IdempotencyKey 就认定同一个值。

### 1.4 在六个位置假设出错

| 停在哪里 | 应怎样理解已有状态 | 不应做的推断 |
|---|---|---|
| 输入校验失败 | 没有越过接受入口 | “Worker 处理失败了” |
| 接受事务中途失败 | 事务内消息/运行/事件/Outbox 回滚 | “一定已经有消息了” |
| 提交后，202 响应丢失 | 服务端可能已经接受，需查询核实 | “网络报错，所以可以安全重发” |
| waiting_approval 时关页 | 等待状态仍可存在 | “关页就是拒绝/取消” |
| E1 已写，最终回复未写 | 业务副作用可能成功，运行未交付 | “没看到回复所以一定没记账” |
| A 失去租约后才返回 | 旧执行结果必须接受提交条件检查 | “结果晚返回，所以一定更新” |

这张表是理解“可恢复”和“幂等”的核心：恢复不能随意重做已发生副作用；它需要来源、状态、键与数据库条件提供证据。

## 2. 完整案例 B：从文件到带证据的制品

假设 U1 上传一份 12 页报告，然后要求制作演示文件。先走上传/解析，再走 Agent 和 Skill；不能把整个过程压缩成“上传→丢给模型→下载”。

### 2.1 沿数据表示追踪

1. 浏览器提交字节与文件名。服务检查文件名、大小、可接受格式等；Blob 保存原文件，数据库记录 Document D1。
2. 解析 Worker 读取 D1 的 Blob。Python 解析页和章节，再按预算切 Chunk。空页、标题、重叠与提取结果都会影响实际片段，不能把“12 页”当成“12 个 Chunk”。
3. 保存稳定来源标识、解析结果与索引；这些写入跨系统，不是一个全局原子事务。出错时查任务状态和具体阶段。
4. 发送消息时用 document_ids 指向 D1；Handler 用当前 userID 验证归属。只知道 D1 不等于可读 D1。
5. 若要回答一个局部问题，Query 检索少数相关片段；若要完整处理全文，使用轮次读取、来源结构与覆盖检查。
6. Graph/Composer 根据已取得的来源组织内容，生成 Skill 输入；Skill 校验输入结构、版本与预算后执行文件算法。
7. 文件内容与元数据通过检查并保存，任务终态条件提交；Agent 恢复后引用真实制品交付。最后仍要检查来源忠实度、文本正确性及实际文件效果。

对应 [文档与 Wiki](06-documents-wiki.md) 和 [Skill 文件交付](08-skill-artifacts.md)。这里说明机制，不声称生成的一定是某种特定页数/布局；真实结果由输入、工具实现和质量检查共同决定。

### 2.2 三种“都读了”的假象

第一种：搜到 5 个高相关片段，就说读完全文。反例是无关但必须保留的附录从未被检索。

第二种：把每轮 coverage 简单相加。两轮共享相同片段时会重复计算。应按稳定来源 ID 求并集，再与需要覆盖的全集比较。

第三种：模型输出写着“已依据全部附件”。这句话不是覆盖证据；要看真实读取结果、遗漏清单、轮次 continuation 和相关质量门禁。

### 2.3 派生 Wiki 的权限练习

W 同时来自 D1 和 D2，Bob 只有 D1 的共享权限。W 即使只有一句话包含 D2 的内容，也不能完整返回给 Bob。随后 D2 被共享，W 才可能满足依赖可见性；D2 删除或来源版本失效后，又必须重新检查 W 是否可读。

这种规则叫来源依赖的访问控制。它防止“原文不能读，但模型摘要把原文泄露了”。对应测试会检查只共享一个来源时聚合页面不可见、完整共享后可见、删除后旧聚合与缓存失效。

## 3. 完整案例 C：一句纠正如何影响下一轮回答

旧事实 M1 为“常住杭州”，active；新输入是明确纠正“我现在常住南京”。以下跟踪的是用户纠正接口/领域 Correct 的链路，不保证每条聊天表达都自动调用这个接口。

1. Service 清理新内容，按 userID 读取 M1；无权访问时不继续。
2. 构造新事实 M2，保留需要的来源/属性，设置 supersedes_id=M1 与新有效时间 t2。
3. Store 开事务，把 M1 改为 superseded，valid_to=t2；要求确实更新到一条仍 active 的旧事实。
4. 检查是否已有相同活动内容，再插入 M2。中间任何失败均触发事务回滚；新事实未成功时旧事实不会因这一事务只关闭一半。
5. 下一次 BuildContext 调用 Recall，结合活动状态、有效期、相关性和预算选择事实。M2 也不是只要存在就每个问题都必定入选。
6. 参考资料附带来源，而 Manifest 只保留诊断所需的标识/计数。模型输入里的“当前明确纠正优先”与领域存储的替代链共同支持一致理解。

阅读时注意 UI 普通修改与 Correct 不等同：前者可能更新同一记录，后者新增替代记录。把所有“更新记忆”笼统说成版本链，会误读真实实现。

## 4. 三个可运行实验：先预测，再运行，再解释

实验代码位于本目录的 examples。Go 与 JavaScript 是简化教学模型，不能证明生产数据库或浏览器行为；Python 另外直接调用真实 conversation_context.py 中两个纯函数。所有实验使用标准库，不需要模型密钥、数据库、浏览器、网络或安装第三方包。

运行命令前需已有对应运行时。实验 Go 模块单独声明 Go 1.22，使用本地 Go；项目主模块则声明 Go 1.26.8。Python 用本项目已有的 3.12+ 环境最省事；JavaScript 文件是 Node 原生模块。命令会使用正常编译缓存；Python 的 -B 禁止生成导入缓存文件。不要把“找不到运行时”误认为业务代码失败。

### 4.1 Go：审批三态与迟到 Worker

阅读 [Go 实验源码](/Users/windcry1/Documents/ai_companion/docs/code-reading/examples/go/main.go)，先猜下面结果：

- `{}` 与 `{"approved":false}` 能否区分？null 又是什么？
- A 的 revision=2、B 接管后 revision=3，A 最后能否写 completed？
- 结果 channel 容量为 1，而接收发生在发送者退出之后，会不会卡住？

在一个终端中执行：

```sh
cd /Users/windcry1/Documents/ai_companion/docs/code-reading/examples/go
GOWORK=off GOPROXY=off GOTOOLCHAIN=local go run .
```

应看到：

```text
字符实验：bytes=7 runes=3
审批实验：{} => missing
审批实验：{"approved":null} => missing
审批实验：{"approved":false} => rejected
审批实验：{"approved":true} => approved
接管实验：A_rev=2 B_rev=3 late_A=false current_B=true
取消实验：context canceled，发送者已退出
Go 实验全部通过（内存教学模型，不是数据库并发测试）
```

按这个顺序逐段读源码：main 的 JSON 表格驱动循环 → Run 字段 → Claim 的条件 → Complete 的同锁检查和写入 → 取消/通道。`must` 是教学断言，不是业务错误处理方式；任何结果偏离预期会立即失败。

Claim 接收手动整数时间，故意不用真实睡眠，保证你每次都能重现“租约有效→过期→接管→旧结果拒绝”。内存 mutex 只是模拟原子边界；生产 Store 使用 SQL 条件与事务，还涉及跨进程并发、持久化、数据库错误等本实验没有覆盖的因素。

最后一个示例依赖容量 1：发送者先把错误放进去，再关闭 done；主函数等 done 后取错误。不要在不理解后果时直接把通道改成无缓冲后运行，它会造成互相等待。若做修改练习，应先设计超时或同步顺序，不要靠强退程序学习生产取消逻辑。

### 4.2 JavaScript：异步顺序、HTTP 分流、消息去重、额度

阅读 [JavaScript 实验源码](/Users/windcry1/Documents/ai_companion/docs/code-reading/examples/web-flow.mjs)，再运行：

```sh
cd /Users/windcry1/Documents/ai_companion
node docs/code-reading/examples/web-flow.mjs
```

应看到：

```text
异步顺序：开始请求 → 调用方继续执行 → 取得响应 → 解析正文
响应分支：409 恢复已有运行；202 按 agent_run/job 分流；缺少运行信息拒绝
消息合并：同 ID 更新，不同 ID 即使同文也保留；原数组未被改写
额度实验：null 继承；0 禁止；-1 不限；并发预检查不等于预留
JavaScript 实验全部通过（纯函数教学模拟，不是浏览器集成测试）
```

fakeFetch 没有访问网络，只返回模拟 Response 形状。第一次 await 后当前函数暂停，因此调用方先记录“继续执行”；Response.ok=false 仍能获得并读取模拟响应。这用来理解检查错误状态的必要性，不是拿模拟函数验证浏览器 fetch 的完整规范。

Map 以 message.id 作为键，所以 M1 可从 sending 更新为 completed，M2 即使内容相同也保留。这个教学合并器没有复刻生产列表的排序、分页和全部字段策略。

额度演示只保留一个字段的三层覆盖。两个请求都读到 used=9，limit=10，都会通过；想解决严格预留需要更强的原子机制，不能靠修改一个比较符号完成。

### 4.3 Python：真实历史选择与诊断清单

阅读 [Python 实验源码](/Users/windcry1/Documents/ai_companion/docs/code-reading/examples/python-context.py)，再运行：

```sh
cd /Users/windcry1/Documents/ai_companion
python3 -B docs/code-reading/examples/python-context.py
```

应看到：

```text
类型实验：cast 不转换对象；bool 也是 int，需要额外排除
复制实验：外层 dict 已复制，嵌套 history 仍共享
真实 history_view：成本 600/800/700，预算 2000，保留 sequence=20,30,30
真实 history_view：最新组成本 2001，仍完整保留；这不替代最终窗口检查
真实 diagnostic_manifest：排除布尔计数和正文，只保留允许字段
覆盖实验：两轮数量相加为 7，去重并集为 6，覆盖 6/6
Python 实验全部通过（两项真实模块函数 + 类型/集合实验，不调用模型）
```

真实 history_view 从新到旧选组，再把组插到选中列表前端恢复时间顺序。最新 sequence=30 的两个气泡是一组，成本 700；再加 sequence=20 成本 800，共 1500；继续加最旧组 600 会超 2000，因此遗漏 1 项。

第二个输入特意让最新组成本 2001：局部选择保留完整最新组，所以仍被选中。之后的整体窗口检查是另一道门，不能从这个结果推导“系统允许任意大的提示词”。

diagnostic_manifest 的原始示例故意含正文与布尔计数，断言它们未进入清理结果。覆盖集合则是独立数学演示，不假称文档 API 会自动替调用方累计轮次状态。

## 5. 从教学实验进入真实测试：读懂测试在证明什么

Go 测试常按三段写：Arrange 构造 Store/时钟/桩 → Act 调用行为 → Assert 检查错误、结果和状态。测试桩是可控制的替身：它用于隔离业务逻辑，不代表已经验证真实数据库、真实模型或网络。

| 测试入口 | 建议先找哪个测试 | 能支持的判断与限制 |
|---|---|---|
| [Agent Service 测试](/Users/windcry1/Documents/ai_companion/internal/agent/service_test.go) | TestAcceptChatCreatesMessageAndRunAsOneStoreOperation | 检查服务构造的消息/运行关联、线程身份、期限与输入；其 Store 是测试替身，不能单独证明真实 SQL 事务隔离 |
| [Agent Worker 测试](/Users/windcry1/Documents/ai_companion/internal/agent/worker_test.go) | TestRuntimeWorkerPausesForApproval | 模拟执行器返回等待审批后，Worker 转移到相应状态；不等于真实浏览器与模型完整审批集成 |
| [共享上下文测试](/Users/windcry1/Documents/ai_companion/internal/conversation/context_snapshot_test.go) | TestSharedContextContainsSummaryRecentGroupsAndSourcedMemory | 构造历史与跨用户事实，检查来源、权限、摘要与最近消息分离、Manifest 不带私文 |
| [上下文预算测试](/Users/windcry1/Documents/ai_companion/internal/contextengine/context_test.go) | TestMemoryBudgetKeepsWholeFactsAndSkipsOversizedCandidates | 完整事实选择、过大项跳过；预算规则不是提供商真实计费器 |
| 同上 | TestFullRequestBudgetCountsToolSchemaAndOutputReserve | 整体请求考虑工具 Schema 与输出预留；通过本地检查不保证远端接受 |
| [Wiki 测试](/Users/windcry1/Documents/ai_companion/internal/document/wiki_test.go) | TestWikiACLIntersectionConflictsAndDeletionInvalidateCache | 构造双来源页面，验证可见性、缓存拷贝隔离和删除失效；成员身份的 HTTP 边界另有测试 |

要深入一个测试，逐行看“测试数据怎样造成分支”比只读测试名字更重要。比如测试使用固定时钟，是为了把 deadline 与过期分支变成可重复事实；测试模拟器返回 waiting_approval，是刻意把模型不确定性排除出 Worker 状态迁移测试。

### 5.1 可选的真实测试命令

下面只列本次阅读直接相关的窄范围入口。需要项目要求的工具链：GOTOOLCHAIN=auto 允许使用本地已缓存的 Go 1.26.8，而不只限于系统默认 go。若工具链或依赖未缓存，GOPROXY=off 会让相应下载需求报错；本章不安排联网安装。依赖/工具链错误不等于断言失败。不要为了读代码运行部署、数据迁移、发布或清库命令。

```sh
cd /Users/windcry1/Documents/ai_companion
GOPROXY=off GOTOOLCHAIN=auto go test ./internal/contextengine
GOPROXY=off GOTOOLCHAIN=auto go test ./internal/agent -run '^(TestAcceptChatCreatesMessageAndRunAsOneStoreOperation|TestRuntimeWorkerPausesForApproval)$' -count=1
```

第二条会编译整个 agent 包及其依赖，但只选中两个命名测试。完整测试还需按仓库环境准备；本次文档工作不等于已执行全仓库所有集成测试。

## 6. 带答案的综合练习

先遮住答案，用“源码位置→条件→结论”回答。只给术语，不解释数据和分支，不算完成。

### 练习一：三段 Token 就是 JWT 吗？

答案：不是。本项目 IssueAccess 返回 v1、Base64URL 声明、HMAC 三段；第一段不是标准 JWT Header。签名有效还需查 Session，声明可解码所以不是加密。证据在 [Token 实现](/Users/windcry1/Documents/ai_companion/internal/identity/token.go:34) 和 Authenticate。

### 练习二：哪个地方证明“晚返回的结果没有覆盖新任务”？

答案：不能只看 Python 调用返回值。需要看 Complete SQL 的 WHERE，是否仍 running、owner 匹配、revision 匹配、租约与 deadline 有效，并检查实际更新结果。第 03 章说明；内存实验只演示这一思想，不替代数据库测试。

### 练习三：为什么账目已经存在，聊天仍可能失败？

答案：领域写入与 Agent 最终交付不是覆盖所有外部动作的一次全局事务。记账完成后可能在网络、恢复或最终消息提交阶段失败。应通过候选/确认键、来源、正式账目 ID 与运行事件核实，不能立即再写一次。

### 练习四：历史预算 10，最新整组成本 12，会选空吗？

答案：当前完整组策略会先保留最新组，即使单组超局部预算；之后仍受最终请求窗口检查。若把它改成空，可能丢失用户刚刚表达的否定或条件；若绕过最终检查，则可能请求超窗。

### 练习五：某项额度用户配置为 0，为何不是“回退默认”？

答案：nil/null 才是这一层未覆盖，0 是明确禁止，-1 是不限额。服务按字段应用套餐→全局→用户覆盖，不是取最小值，也不是判断真值后覆盖。

### 练习六：哪些“验证通过”依然不能证明业务正确？

答案：JSON 能解析不证明参数有权限；Schema 通过不证明事实正确；模型有回答不证明动作执行；文件字节保存不证明终态提交；任务 completed 不证明视觉效果完美；文档链接校验通过不证明人工解释没有逻辑错误。每一种验证都有自己的范围。

## 7. 分阶段学习清单

不按固定天数安排，用能解释出的结果作为进度。

| 阶段 | 读什么 | 不看文档也应能完成的动作 |
|---|---|---|
| 1 语法地基 | 第 00 章、Go JSON 实验 | 拆函数签名，解释 nil/false、错误返回、指针、切片 |
| 2 登录身份 | 第 01 章 | 画出 User/Device/Session/Token 的关系，指出撤销检查 |
| 3 聊天闭环 | 第 02、03、11、04 章 | 从发送到最终消息逐个说出对象、状态和可信边界 |
| 4 上下文与知识 | 第 05、06 章、Python 实验 | 计算预算、区分局部/全量读取、说明来源与权限 |
| 5 领域与制品 | 第 07、08 章 | 区分候选/正式结果/任务，解释幂等键与文件交付 |
| 6 多用户与可靠性 | 第 09、10 章、JavaScript 实验 | 解释权限/额度、故障窗口、降级与配置快照 |
| 7 反向证明 | 本章测试入口 | 根据一个断言找到生产条件，再构造能走失败分支的输入 |

遇到没讲到的函数，用同一方法继续：调用者、输入可信度、提前返回、写入边界、后续消费者、测试。当前文档覆盖关键行为链路与所选源码，不是全仓库每个依赖内部、测试、部署配置和文件格式算法的逐行全集；索引给出明确覆盖数，不用“全部讲完”掩盖范围。

## 8. 维护说明

源码摘录与行号由 build.mjs 生成；逐行解释在 guide.mjs，章节预备课与案例在 teaching.mjs，变量/函数卡片在 symbols.mjs，速查手册前言在 authoring/symbol-primer.md。本章和第 00 章手工维护。改业务源码后要先重新核对讲解，再生成，不能只移动行号冒充语义仍然正确。移动仓库时，本章与第 00 章中的绝对链接和命令路径也需手动调整。

在仓库根目录执行下面命令，校验当前生成文档、源码指纹、摘录与本地跳转：

```sh
node docs/code-reading/build.mjs --check
```

此命令不运行真实业务，不验证密码学实现或生产部署。三个实验和选定真实测试是另一层验证，应该分别记录，不应把一次文档检查表述成“整个项目测试通过”。

### 本次实际验证记录（2026-09-23）

| 检查 | 结果 | 范围 |
|---|---|---|
| Go、JavaScript、Python 三个教学实验 | 全部通过 | 内存模拟与两项真实 Python 纯函数，无外部业务调用 |
| internal/contextengine 包测试 | 通过 | 本地共享上下文契约、事实预算与请求窗口相关单元测试 |
| 上述两个命名 Agent 测试 | 通过 | 接受消息/运行关联与等待审批状态迁移；测试替身，不是真实模型集成 |
| 文档生成与 --check | 通过 | 源码摘录、指纹、生成内容与本地文件链接 |

真实 Go 包测试使用本机已缓存的 Go 1.26.8。初次强制使用系统本地 Go 1.26.4 时因低于项目要求退出；切换到已有缓存工具链后通过，没有修改项目版本要求，也没有据此改业务代码。

[返回阅读总入口](README.md) · [变量与函数速查](13-symbol-reference.md)
