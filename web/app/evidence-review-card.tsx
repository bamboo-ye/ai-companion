import { evidenceReviewLabel, evidenceReviewReason, type EvidenceReview } from "./evidence-review";

export function EvidenceReviewCard({ review, historical = false }: { review: EvidenceReview; historical?: boolean }) {
  return <aside className="evidenceReview" aria-label="本次资料核验">
    <strong>{evidenceReviewLabel(review)}</strong>
    <p>{review.resolved_issues} / {review.total_issues} 项分歧已解决 · {review.rounds} 轮核验 · 补充读取 {review.additional_reads} 次</p>
    <details><summary>查看核验说明</summary>
      <p>{evidenceReviewReason(review.termination_reason)}</p>
      <p>{review.isolation_passed ? "首轮分别判断，汇合后核对依据。" : "未完成有效的独立核验。"}
        {review.sources_verified ? historical ? "交付时来源可访问且版本匹配。" : "来源可访问且版本匹配。" : "来源尚未通过当前访问与版本复验。"}</p>
      {historical && <p>这是该回复交付时的核验记录；资料可能已变化，需要最新结论时请重新核验。</p>}
      <small>判断一致不等于事实已被证明，请结合原文与适用条件使用。</small>
    </details>
  </aside>;
}
