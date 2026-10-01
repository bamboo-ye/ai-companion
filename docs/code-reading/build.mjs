import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import chapters from './guide.mjs';
import teaching from './teaching.mjs';
import symbols from './symbols.mjs';

// 文档机械生成器：只摘录真实源码、添加作者编写的解释；不推测代码含义。
const directory = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(directory, '../..');
const check = process.argv.includes('--check');
const sources = new Map();
let totalLines = 0;
let totalNotes = 0;
let symbolSections = 0;
let variableEntries = 0;
const declarations = new Set();
const referenceName = '13-symbol-reference.md';
let symbolReference = fs.readFileSync(path.join(directory, 'authoring/symbol-primer.md'), 'utf8');
const outputs = new Map();
// 这两篇是人工维护的教材，不由生成器改写；同样纳入本地链接校验。
const companionNames = ['00-foundations.md', '12-worked-cases-and-labs.md'];
for (const name of companionNames) {
  outputs.set(name, fs.readFileSync(path.join(directory, name), 'utf8'));
}
const link = (file, line) => `[${file}${line ? `:${line}` : ''}](${path.join(root, file)}${line ? `:${line}` : ''})`;
const names = chapters.map((c, i) => `${String(i + 1).padStart(2, '0')}-${c.slug}.md`);
const prose = value => value.replace(/\b(?:internal|web|workers|cmd|docs)\/[A-Za-z0-9_./-]+\.(?:go|py|tsx|ts|mjs|md)\b/g, file => {
  if (!fs.existsSync(path.join(root, file))) throw new Error(`Missing prose reference: ${file}`);
  return link(file);
});
const escaped = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const cell = value => value.replaceAll('|', '\\|').replaceAll('\n', '<br>');
const declarationLine = (source, name, first, last) => {
  const symbol = escaped(name);
  const pattern = new RegExp(`^\\s*(?:func\\s+(?:\\([^)]*\\)\\s+)?${symbol}\\s*\\(|type\\s+${symbol}\\b|(?:async\\s+)?function\\s+${symbol}\\s*\\(|(?:async\\s+)?def\\s+${symbol}\\s*\\()`);
  const candidates = source.lines.flatMap((line, i) => pattern.test(line) ? [i + 1] : []);
  if (!candidates.length) throw new Error(`Missing function/type declaration: ${name}`);
  return candidates.find(n => n >= first && n <= last) ?? candidates.filter(n => n <= first).at(-1) ?? candidates[0];
};

for (const [index, chapter] of chapters.entries()) {
  const lesson = teaching[chapter.slug];
  if (!lesson?.before || !lesson?.after) throw new Error(`Missing beginner lesson: ${chapter.slug}`);
  const cards = symbols[chapter.slug];
  if (!cards || cards.length !== chapter.sections.length) throw new Error(`Symbol coverage mismatch: ${chapter.slug}`);
  symbolReference += `\n## ${String(index + 1).padStart(2, '0')} ${chapter.title}\n\n| 函数/类型声明 | 对应行为与变量解释 |\n|---|---|\n`;
  let output = `# ${String(index + 1).padStart(2, '0')} ${chapter.title}\n\n[阅读总入口](README.md)\n\n${prose(chapter.intro)}\n`;
  output += '\n每段源码前新增函数卡片和变量表；常见短名、标准库调用及同名对象区别见[变量与函数速查](13-symbol-reference.md)。\n';
  output += `\n## 初学者预备课：先理解问题\n\n${prose(lesson.before)}\n`;
  for (const [sectionIndex, section] of chapter.sections.entries()) {
    const { file, title, notes, intro = '', after = '' } = section;
    if (!sources.has(file)) {
      const raw = fs.readFileSync(path.join(root, file), 'utf8');
      sources.set(file, { raw, lines: raw.split('\n'), sha256: crypto.createHash('sha256').update(raw).digest('hex') });
    }
    const source = sources.get(file);
    const rows = notes.trim().split('\n').map(row => {
      const match = row.match(/^(\d+)(?:-(\d+))?\|(.+)$/);
      if (!match) throw new Error(`Invalid annotation: ${file}: ${row}`);
      const first = Number(match[1]);
      const last = Number(match[2] || match[1]);
      if (first > last || last > source.lines.length) throw new Error(`Invalid range: ${row}`);
      return { first, last, description: match[3] };
    });
    rows.forEach((row, i) => {
      if (i && row.first !== rows[i - 1].last + 1) throw new Error(`Annotation gap/overlap: ${file}:${row.first}`);
    });
    output += `\n## ${title}\n\n源码：${link(file, rows[0].first)}\n\n${intro ? `${prose(intro)}\n\n` : ''}`;
    const card = cards[sectionIndex];
    if (!card.names.length || !card.contract || !card.calls) throw new Error(`Incomplete symbol card: ${file}:${title}`);
    const anchors = card.names.map(name => {
      const line = declarationLine(source, name, rows[0].first, rows.at(-1).last);
      declarations.add(`${file}:${line}:${name}`);
      return `[\`${name}\`](${path.join(root, file)}:${line})`;
    });
    const variables = card.variables.trim().split('\n').map(row => {
      const fields = row.split('|');
      if (fields.length !== 4 || fields.some(value => !value.trim())) throw new Error(`Invalid variable row: ${file}: ${row}`);
      // 检查讲解的标识符确实存在。类型和语义仍须人工核对，不能由出现过这个名字证明。
      for (const name of fields[0].match(/[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*/g) ?? []) {
        if (!new RegExp(`\\b${escaped(name)}\\b`).test(source.raw)) throw new Error(`Missing variable/field: ${file}:${name}`);
      }
      return fields;
    });
    if (!variables.length) throw new Error(`Missing variable table: ${file}:${title}`);
    const anchor = `symbols-${index + 1}-${sectionIndex + 1}`;
    output += `<a id="${anchor}"></a>\n\n### 函数与变量详解\n\n函数/类型声明：${anchors.join('、')}。\n\n**职责、输入输出与影响：** ${card.contract}\n\n**调用关系：** ${card.calls}\n\n`;
    output += '| 变量/字段 | 类型或数据形状 | 来源或初值 | 具体含义与变化 |\n|---|---|---|---|\n';
    output += variables.map(([name, ...rest]) => `| \`${cell(name)}\` | ${rest.map(cell).join(' | ')} |`).join('\n') + '\n\n### 源码与逐行解释\n\n';
    symbolReference += `| ${anchors.join('、')} | [${cell(title)}](${names[index]}#${anchor}) |\n`;
    symbolSections++;
    variableEntries += variables.length;
    let batch = [];
    const flush = () => {
      if (!batch.length) return;
      const first = batch[0].first;
      const last = batch.at(-1).last;
      output += '```text\n' + source.lines.slice(first - 1, last).map((line, i) => `${String(first + i).padStart(4, ' ')} │ ${line}`).join('\n') + '\n```\n\n';
      output += batch.map(r => `- **L${r.first}${r.last === r.first ? '' : `–L${r.last}`}**：${r.description}`).join('\n') + '\n\n';
      totalLines += last - first + 1;
      totalNotes += batch.length;
      batch = [];
    };
    for (const row of rows) {
      if (batch.length && row.last - batch[0].first >= 20) flush();
      batch.push(row);
    }
    flush();
    if (after) output += `\n${prose(after)}\n`;
  }
  output += `\n## 把代码连成一次真实行为\n\n${prose(lesson.after)}\n`;
  if (chapter.end) output += `\n## 对照阅读与验证\n\n${prose(chapter.end)}\n`;
  output += `\n[返回目录](README.md)${index + 1 < chapters.length ? ` · [下一章：${chapters[index + 1].title}](${names[index + 1]})` : ' · [下一章：完整案例与实验](12-worked-cases-and-labs.md)'}\n`;
  outputs.set(names[index], output);
}
symbolReference += `\n## 本版覆盖与校验\n\n覆盖 ${symbolSections} 个源码阅读段、${declarations.size} 个不同声明位置的函数/类型、${variableEntries} 条变量/字段条目（一个条目可能对照多个关联名字，不等于唯一变量数量）。所有卡片与原章节共用同一份人工数据。\n\n生成器检查章节卡片齐全、函数/类型声明存在、变量标识符存在、源码指纹及本地跳转；这些机械检查不等于自动证明类型推断与语义正确。本次仅增补文档，不改变运行时业务。\n\n[返回阅读总入口](README.md)\n`;
outputs.set(referenceName, symbolReference);

const readme = `# 按行为链路阅读源码：逐行讲解\n\n这套文档对应 2026-09-23 工作区，基线提交为 324d432。按“用户做一件事 → HTTP 入口 → 领域函数 → 数据库事务 → Worker → 前端展示”的顺序组织。源码直接从工作区摘录，行号是原文件的物理行号，不是示意代码。\n\n覆盖 ${chapters.length} 章、${sources.size} 个源码文件、${totalLines} 行源码摘录、${totalNotes} 条人工解释。阅读单位是关键业务函数；导入、重复的 CRUD 包装、单纯括号和长参数列表合并讲解。没有把整个仓库的每个测试、样式、迁移和 7,000 多行 Python Graph 全文复制进来；各章末尾提供继续追踪的文件和验证入口。\n\n每段最多约 20 行真实代码，紧跟对应行号解释。一个物理行包含多条 TypeScript 语句时，解释按该行的实际执行顺序展开。左侧行号和竖线只供阅读，不能直接复制为可编译代码。\n\n## 推荐阅读顺序\n\n| 章 | 用户或系统行为 | 重点 |\n|---|---|---|\n${chapters.map((c, i) => `| ${i + 1} | [${c.title}](${names[i]}) | ${c.focus} |`).join('\n')}\n\n第一次阅读先完成 1 → 2 → 3 → 4，贯通一次发送消息；再读 5 理解上下文，按兴趣选择文档、生活、技能等领域。最后两章解释权限、配额、发布和故障恢复。\n\n## 读 Go 时反复出现的写法\n\n| 写法 | 在这个项目里的意思 |\n|---|---|\n| Handler → Service → Store | Handler 处理 HTTP 和身份；Service 执行业务规则；Store 负责 SQL、事务和并发约束。 |\n| ctx context.Context | 携带取消、超时和 Trace；不要把它等同于模型上下文。 |\n| value, err := ... | 接收结果并显式检查错误；错误返回常常就是当前行为的终止分支。 |\n| errors.Is / errors.As | 判断领域错误或提取结构化错误类型，决定 404、409、重试或终态。 |\n| interface + 类型断言 | 依赖最小能力，例如只有实现 WikiStore 才能编译 Wiki。 |\n| *bool / *int | 区分未提供、false/0 和真实值；审批和配额尤其依赖此区别。 |\n| defer tx.Rollback() | 任何提前返回都回滚；成功 Commit 后延迟 Rollback 不会撤销提交。 |\n| revision / expected | 乐观并发版本；旧页面或旧 Worker 不能覆盖新状态。 |\n| lease_owner / lease_expires_at | 有时限的执行权；任务可被接管，结果提交必须重新检查。 |\n| true/false, error | 不同方法含义不同：可能是“是否领取过任务”“是否新建”“是否改变状态”；每节会说明。 |\n| UTC + timezone | 数据时间统一保存，用户输入解释和日/月边界按本地时区计算。 |\n\n## 四种 ID 要分清\n\n- Conversation ID：多轮对话容器。\n- Message ID：一次发言或一个气泡；助手通过 ReplyToID 关联原用户消息。\n- Agent Run / Generation Job ID：处理一次输入的运行记录。它可失败、取消、等待或恢复。\n- Skill Run ID：Agent 某一步实际工具任务；一个 Agent Run 可产生多个 Skill Run。\n\n“运行 completed”只描述对应那一层。ToolPreparation.status=completed 不代表后台 Skill 已完成；文档已 queued 不代表已经解析；HTTP 202 不代表业务已经成功。各章专门指出这些状态边界。\n\n## 与前次概览不同的当前实现\n\n当前消息 intake 已共用 BuildContext；记忆支持可选语义分数和纠正链；文档有 Wiki 编译、依赖可见性与编辑版本；管理员 Web 登录已有 Passkey；额度按用户、全局、套餐三层覆盖。阅读时以本套摘录及源码为准，旧概览的“只有词法记忆/没有 Wiki/后台仅管理 Key”不再完整。语义检索仍需要配置启用，不能据此假设本机当前启用了外部 Embedding。\n\n## 文档与源码一致性\n\n源码链接采用当前工作区绝对路径，适合在本机点击对照；文件名与函数名也保留在正文。移动仓库后重新生成可更新链接。source-manifest.json 保存本次涉及文件的 SHA-256。\n\n从仓库根目录执行：\n\n\`\`\`sh\nnode docs/code-reading/build.mjs --check\n\`\`\`\n\n该命令只检查生成文档、摘录和源文件指纹是否一致；它不执行业务，也不证明人工解释一定正确。若代码变化，先核对 guide.mjs 中行号与解释，再执行不带 --check 的命令更新摘录。不要在代码已经偏移时直接刷新文档冒充解释仍然有效。\n\n[已有技术指南](../TECHNICAL_GUIDE.zh-CN.md) · [上下文专题](../CONTEXT_MANAGEMENT.md) · [项目完成边界](../PROJECT_COMPLETION.md)\n`;
outputs.set('README.md', readme
  .replace(`覆盖 ${chapters.length} 章、`, `共 ${chapters.length + companionNames.length + 1} 章：${chapters.length} 个源码链路章节、2 个基础/实践章节与 1 个变量/函数速查章节；源码摘录覆盖 `)
  .replace('## 推荐阅读顺序', `## 初学者从这里开始\n\n新增的讲解层次是“基础语法 → 业务问题 → 原码与行号 → 变量和状态演算 → 错误分支 → 自测”。不要求先掌握所有框架；遇到术语可随时返回第 00 章。\n\n- [第 00 章：从零读懂三种语言、HTTP、SQL、异步和模型上下文](00-foundations.md)：读函数的方法、真实语法拆解、关键技术的原理与边界。\n- 第 01–11 章：每章新增预备课、输入到输出的具体推演、设计理由、常见误解和自测答案；保留真实源码逐行/小段解释。\n- [第 12 章：完整案例、三种语言实验与源码学习路线](12-worked-cases-and-labs.md)：逐步跟踪一次工具审批、一次文件处理、一次记忆纠正，以及不连接数据库或模型的小实验。\n\n零基础建议：00 → 01 → 02 → 03 → 11 → 04 → 05 → 06 → 07 → 08 → 09 → 10 → 12。先讲出“谁调用谁、数据在哪里、失败会怎样”，再记住具体语法。\n\n## 推荐阅读顺序`)
  .replace('最后两章解释权限、配额、发布和故障恢复。', '第 9、10 章解释权限、配额、发布和故障恢复。第 11 章可接在第 3 章之后阅读，补齐 Go → Python → 模型请求的内部调用。')
  .replace('## 与前次概览不同的当前实现', '## 当前实现与配置边界')
  .replace('原码与行号', '源码与行号')
  .replace('## 推荐阅读顺序', `## 本次新增：变量和函数的具体解释\n\n- [第 13 章：变量与函数速查手册](13-symbol-reference.md)：同名变量辨析、常用函数输入输出、完整函数导航。\n- 已给全部 ${symbolSections} 个源码段加入函数卡片，明确职责、参数/返回值、上下游调用与副作用。\n- 增加 ${variableEntries} 条变量/字段解释，说明类型、来源/初值、用途与变化；可直接在原章节与代码对照，不必只读附录。\n\n## 推荐阅读顺序`)
  .replace('移动仓库后重新生成可更新链接。', '移动仓库后，生成章节的源码链接可通过重新生成更新；第 00、12 章是手工教材，其中的绝对链接和示例命令路径需同步调整。')
  .replace('阅读时以本套摘录及源码为准，旧概览的“只有词法记忆/没有 Wiki/后台仅管理 Key”不再完整。', '这些能力都按本次工作区源码核对。')
  .replace('先核对 guide.mjs 中行号与解释', '先核对 guide.mjs 中行号与解释、teaching.mjs 中的案例，以及 symbols.mjs 中的变量/函数卡片')
);
outputs.set('source-manifest.json', JSON.stringify({ baseline: '324d432', date: '2026-09-23', sourceLines: totalLines, annotations: totalNotes, symbolSections, functionTypeDeclarations: declarations.size, variableEntries, sources: [...sources].map(([file, s]) => ({ file, sha256: s.sha256 })) }, null, 2) + '\n');

let linkCount = 0;
for (const [name, content] of outputs) {
  if (!name.endsWith('.md')) continue;
  const markdown = content.replace(/```[^\n]*\n[\s\S]*?\n```/g, '');
  for (const match of markdown.matchAll(/\]\(([^)]+)\)/g)) {
    const target = match[1];
    if (/^(?:https?:|#)/.test(target)) continue;
    const position = target.match(/^(.*):(\d+)$/);
    const file = position ? position[1] : target.split('#')[0];
    const resolved = path.resolve(directory, file);
    if (!outputs.has(path.relative(directory, resolved)) && !fs.existsSync(resolved)) throw new Error(`Broken link in ${name}: ${target}`);
    if (position && Number(position[2]) > fs.readFileSync(resolved, 'utf8').split('\n').length) throw new Error(`Invalid line in ${name}: ${target}`);
    if (target.includes('#symbols-')) {
      const anchor = target.split('#')[1];
      const content = outputs.get(path.relative(directory, resolved));
      if (!content?.includes(`id="${anchor}"`)) throw new Error(`Missing symbol anchor in ${name}: ${target}`);
    }
    linkCount++;
  }
}

for (const [name, content] of outputs) {
  const target = path.join(directory, name);
  if (check) {
    if (!fs.existsSync(target) || fs.readFileSync(target, 'utf8') !== content) throw new Error(`Document/source mismatch: ${name}`);
  } else fs.writeFileSync(target, content);
}
console.log(`${check ? 'Verified' : 'Generated'} ${chapters.length} source chapters, ${sources.size} source files, ${totalLines} source lines, ${totalNotes} annotations, ${symbolSections} symbol cards, ${declarations.size} declarations, ${variableEntries} variable entries, ${linkCount} valid links.`);
