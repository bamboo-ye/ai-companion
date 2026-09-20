import assert from "node:assert/strict";
import test from "node:test";
import { quotaDraft, quotaLimits } from "./ops/quota-input.ts";

test("quota amounts preserve inheritance, zero, unlimited and exact USD micros", () => {
  const draft = quotaDraft();
  draft.documents = { mode: "custom", value: "0" };
  draft.agent_runs_per_month = { mode: "unlimited", value: "" };
  draft.model_cost_micros_monthly = { mode: "custom", value: "12.345678" };
  const result = quotaLimits(draft);
  assert.equal(result.documents, 0);
  assert.equal(result.workspaces, null);
  assert.equal(result.agent_runs_per_month, -1);
  assert.equal(result.model_cost_micros_monthly, 12_345_678);
  result.model_cost_micros_monthly = Number.MAX_SAFE_INTEGER;
  assert.deepEqual(quotaLimits(quotaDraft(result)), result);
});

test("quota input rejects fractional counts, missing amounts and precision loss", () => {
  for (const value of ["", "-1", "1.2", "1e3", "9007199254740992"]) {
    const draft = quotaDraft(); draft.documents = { mode: "custom", value };
    assert.throws(() => quotaLimits(draft));
  }
  for (const value of ["0.0000001", "9007199254.740992"]) {
    const draft = quotaDraft(); draft.model_cost_micros_monthly = { mode: "custom", value };
    assert.throws(() => quotaLimits(draft));
  }
});
