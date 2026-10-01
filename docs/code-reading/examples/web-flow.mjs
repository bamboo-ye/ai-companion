// 教学模拟：不启动浏览器、不访问 HTTP、不导入生产 React 组件。
import assert from 'node:assert/strict';

// 用已经解决的 Promise 模拟 HTTP 返回，隔离 await 的执行顺序。
const order = [];
async function fakeFetch() {
  return { ok: false, status: 409, json: async () => ({ agent_run: { id: 'R1' } }) };
}
async function request() {
  order.push('开始请求');
  const response = await fakeFetch();
  order.push('取得响应');
  const payload = await response.json();
  assert.equal(response.ok, false);
  assert.equal(payload.agent_run.id, 'R1');
  order.push('解析正文');
  return response.status;
}
const pending = request();
order.push('调用方继续执行');
assert.equal(await pending, 409);
assert.deepEqual(order, ['开始请求', '调用方继续执行', '取得响应', '解析正文']);
console.log(`异步顺序：${order.join(' → ')}`);

// 只是把聊天入口的响应分支抽成纯函数；不是整个真实接口的替代实现。
function actionFor(status, payload) {
  if (status === 409 && payload.agent_run) return 'observe-existing';
  if (status < 200 || status >= 300) throw new Error(`HTTP ${status}`);
  if (payload.agent_run) return 'poll-agent';
  if (payload.job) return 'observe-job-events';
  throw new Error('missing run/job');
}
assert.equal(actionFor(409, { agent_run: { id: 'R1' } }), 'observe-existing');
assert.equal(actionFor(202, { agent_run: { id: 'R2' } }), 'poll-agent');
assert.equal(actionFor(202, { job: { id: 'J1' } }), 'observe-job-events');
assert.throws(() => actionFor(500, {}), /HTTP 500/);
assert.throws(() => actionFor(202, {}), /missing run\/job/);
console.log('响应分支：409 恢复已有运行；202 按 agent_run/job 分流；缺少运行信息拒绝');

function mergeByID(previous, incoming) {
  const byID = new Map(previous.map(item => [item.id, item]));
  for (const item of incoming) byID.set(item.id, { ...byID.get(item.id), ...item });
  return [...byID.values()];
}
const previous = [{ id: 'M1', content: '你好', status: 'sending' }];
const merged = mergeByID(previous, [
  { id: 'M1', content: '你好', status: 'completed' },
  { id: 'M2', content: '你好', status: 'completed' },
]);
assert.equal(merged.length, 2);
assert.equal(merged[0].status, 'completed');
assert.equal(previous[0].status, 'sending');
console.log('消息合并：同 ID 更新，不同 ID 即使同文也保留；原数组未被改写');

function effectiveLimit(plan, global, user) {
  let value = plan;
  if (global != null) value = global; // 在此同时表示 null/undefined 未覆盖。
  if (user != null) value = user;
  return value;
}
function blocked(used, limit) {
  return limit >= 0 && used >= limit;
}
assert.equal(effectiveLimit(100, 60, null), 60);
assert.equal(effectiveLimit(100, 60, 0), 0);
assert.equal(effectiveLimit(100, 60, -1), -1);
assert.equal(blocked(0, 0), true);
assert.equal(blocked(100000, -1), false);
// 两个请求都读到 used=9；接受前检查不是预留，两个均通过。
const checks = [blocked(9, 10), blocked(9, 10)];
assert.deepEqual(checks, [false, false]);
console.log('额度实验：null 继承；0 禁止；-1 不限；并发预检查不等于预留');
console.log('JavaScript 实验全部通过（纯函数教学模拟，不是浏览器集成测试）');
