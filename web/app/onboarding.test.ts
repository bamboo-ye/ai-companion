import assert from "node:assert/strict";
import test from "node:test";
import { adminGuideTopics, searchGuideTopics, userGuideTopics } from "./onboarding-content.ts";
import { guideStorageKey, parseGuideProgress } from "./onboarding-state.ts";

test("corrupt or unavailable saved progress never prevents a first visit", () => {
  for (const raw of [null, "not json", "null", "[]", "42", '"text"']) {
    assert.deepEqual(parseGuideProgress(raw, ["welcome", "chat"]), { dismissed: false, read: [], lastTopic: "welcome" });
  }
});

test("restored progress drops removed topics and duplicates and rejects invalid field types", () => {
  assert.deepEqual(parseGuideProgress(JSON.stringify({ dismissed: "false", read: ["chat", "chat", "removed", 1, null], lastTopic: "removed" }), ["welcome", "chat"]), {
    dismissed: false, read: ["chat"], lastTopic: "welcome",
  });
  assert.deepEqual(parseGuideProgress('{"dismissed":true,"read":"chat","lastTopic":"chat"}', ["welcome", "chat"]), {
    dismissed: true, read: [], lastTopic: "chat",
  });
});

test("user and administrator progress are stored separately and can resume", () => {
  const storage = new Map([[guideStorageKey("user"), JSON.stringify({ dismissed: true, read: ["welcome"], lastTopic: "chat" })]]);
  assert.equal(parseGuideProgress(storage.get(guideStorageKey("user")) ?? null, ["welcome", "chat"]).lastTopic, "chat");
  assert.equal(parseGuideProgress(storage.get(guideStorageKey("admin")) ?? null, ["welcome"]).dismissed, false);
});

test("guide search matches examples, prerequisites, routes and mixed case keywords", () => {
  assert.ok(searchGuideTopics(userGuideTopics, "昨晚打车").some((topic) => topic.id === "life"));
  assert.ok(searchGuideTopics(userGuideTopics, "  pptx   大纲 ").some((topic) => topic.id === "presentations"));
  assert.ok(searchGuideTopics(adminGuideTopics, "  mFa 发布 ").some((topic) => topic.id === "configuration"));
  assert.ok(searchGuideTopics(adminGuideTopics, "配置中心 收敛").some((topic) => topic.id === "runtime"));
  assert.equal(searchGuideTopics(userGuideTopics, "totally-missing-feature").length, 0);
  assert.equal(searchGuideTopics(userGuideTopics, "  ").length, userGuideTopics.length);
});

test("every guide chapter has a unique id and its navigation stays within the correct frontend", () => {
  for (const { topics, targets } of [
    { topics: userGuideTopics, targets: ["companion", "life", "work", "memories", "work-tools", "task-history", "documents", "profile"] },
    { topics: adminGuideTopics, targets: ["observability", "logs", "incidents", "performance", "configuration", "studio"] },
  ]) {
    assert.equal(new Set(topics.map((topic) => topic.id)).size, topics.length);
    assert.equal(topics[0].id, "welcome");
    for (const target of targets) assert.ok(topics.some((topic) => topic.target === target), `Missing guide for ${target}`);
    for (const topic of topics) {
      assert.ok(topic.steps.length > 0 && topic.path && topic.tip);
      if (topic.target) assert.ok(targets.includes(topic.target) && topic.action, `Invalid navigation in ${topic.id}`);
    }
  }
});
