import { notFound } from "next/navigation";
import { EvidenceReviewPreview } from "./preview";

export default function Page() {
  if (process.env.NODE_ENV !== "development" || process.env.DEBATE_PREVIEW !== "true") notFound();
  return <EvidenceReviewPreview />;
}
