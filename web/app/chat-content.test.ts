import assert from "node:assert/strict";
import test from "node:test";

import { confirmationPrompt, executionResultNotice, visibleChatText, productKnowledgeSegments } from "./chat-content.ts";

const generatedResult = `工作任务已执行完成。
<!--ai-generated-file:20acbdb0-49ab-4fb5-8263-f338f3a94a27|df451b72-8599-4e2f-9c80-3d7bc46af326|体育课程表整理-Semester-A-2026-2027.pptx-->
<!--ai-skill-run:20acbdb0-49ab-4fb5-8263-f338f3a94a27|1|succeeded-->`;

test("visibleChatText removes internal task and file metadata", () => {
  assert.equal(visibleChatText(generatedResult), "工作任务已执行完成。");
});

test("confirmation and result notices never expose internal metadata", () => {
  const confirmation = confirmationPrompt(`工具：office.docx_edit\n操作：等待确认\n<!--ai-skill-run:run-1|1|waiting_confirmation-->`);
  assert.equal(confirmation, "确认执行以下更新吗？\n\n工具：office.docx_edit\n操作：等待确认");
  assert.equal(executionResultNotice(generatedResult), "执行结果：工作任务已执行完成。");
  assert.doesNotMatch(confirmation, /<!--ai-/);
});

test("product knowledge citations link to the authenticated reader without arbitrary HTML or URLs", () => {
  assert.deepEqual(productKnowledgeSegments("说明：[产品指南：记忆](/#knowledge=builtin-user-memories) 完成。"), [
    { text: "说明：" }, { text: "产品指南：记忆", href: "/#knowledge=builtin-user-memories" }, { text: " 完成。" },
  ]);
  for (const text of ["[bad](javascript:alert(1))", "[bad](https://example.com/#knowledge=builtin-user-welcome)", "<script>bad()</script>"]) {
    assert.deepEqual(productKnowledgeSegments(text), [{ text }]);
  }
});
