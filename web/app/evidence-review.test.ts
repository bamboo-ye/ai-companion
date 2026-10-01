import assert from "node:assert/strict";
import test from "node:test";
import { evidenceReviewLabel, evidenceReviewReason, parseEvidenceReview, restoreEvidenceReview, latestEvidenceReviewRun, type EvidenceReview } from "./evidence-review.ts";

test("completed calls do not present unresolved evidence as verified", () => {
  const review = { status: "unresolved", termination_reason: "no_new_evidence" } as EvidenceReview;
  assert.equal(evidenceReviewLabel(review), "依据不足，已保留分歧");
  assert.match(evidenceReviewReason(review.termination_reason), /没有找到/);
});

const complete: EvidenceReview = {status: "resolved", total_issues: 1, resolved_issues: 1, rounds: 2, additional_reads: 1, isolation_passed: true, sources_verified: true, termination_reason: "resolved"};
const messages = [{id: "answer", role: "assistant", content: "已回答\n<!--ai-agent-run:run-1|completed|4-->"}];
const savedRun = {id: "run-1", conversation_id: "conversation-1", status: "completed", output: {evidence_review: complete}};

test("restores a persisted review only to its conversation and answer", async () => {
  const requested: string[] = [];
  assert.deepEqual(await restoreEvidenceReview(messages, "conversation-1", async id => {requested.push(id);return savedRun;}), {runID: "run-1", review: complete});
  assert.deepEqual(requested, ["run-1"]);
  assert.equal(await restoreEvidenceReview(messages, "another-conversation", async () => savedRun), null);
  assert.equal(await restoreEvidenceReview(messages, "conversation-1", async () => ({...savedRun, id: "another-run"})), null);
  assert.equal(await restoreEvidenceReview(messages, "conversation-1", async () => ({...savedRun, status: "running"})), null);
});

test("new requests, user-inserted markers, and ordinary replies never inherit older verification", () => {
  for (const last of [{id: "user", role: "user", content: messages[0].content}, {id: "plain", role: "assistant", content: "普通回复"}]) {
    assert.equal(latestEvidenceReviewRun([...messages, last]), null);
  }
  assert.equal(latestEvidenceReviewRun([]), null);
  assert.deepEqual(latestEvidenceReviewRun([{...messages[0], content: "<!--ai-agent-run:fake|completed|1-->\n正文\n" + messages[0].content}]), {runID: "run-1", messageID: "answer"});
});

test("unavailable and malformed persisted records degrade to no card", async () => {
  for (const output of [null, {}, {evidence_review: {...complete, resolved_issues: 99}}]) {
    assert.equal(await restoreEvidenceReview(messages, "conversation-1", async () => ({...savedRun, output})), null);
  }
});

test("invalid counters and loosely typed booleans cannot produce a trusted card", () => {
  for (const change of [{rounds: 3}, {resolved_issues: -1}, {total_issues: 0}, {additional_reads: 0.5}, {sources_verified: "true"}]) {
    assert.equal(parseEvidenceReview({...complete, ...change}), null);
  }
  const safe = parseEvidenceReview({...complete, private_quote: "SECRET"});
  assert.ok(safe);
  assert.ok(!JSON.stringify(safe).includes("SECRET"));
});

test("source and independence validation override a persisted success", () => {
  for (const change of [{sources_verified: false}, {isolation_passed: false}, {termination_reason: "independence_failed"}, {termination_reason: "source_version_changed"}, {termination_reason: "snapshot_changed"}]) {
    const safe = parseEvidenceReview({...complete, ...change});
    assert.equal(safe?.resolved_issues, 0);
    assert.equal(safe?.status, "unresolved");
    assert.notEqual(safe?.termination_reason, "resolved");
  }
});
test("source invalidation overrides a previous resolved label", () => {
  assert.equal(evidenceReviewLabel({ status: "resolved", termination_reason: "evidence_access_changed" } as EvidenceReview), "资料已变化，需要重新核验");
  assert.ok(!evidenceReviewReason("private exception details").includes("private"));
});
