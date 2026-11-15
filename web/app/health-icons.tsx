import type { ReactNode } from "react";

type IconProps = { size?: number; className?: string };

function icon(paths: ReactNode) {
  return function HealthIcon({ size = 20, className }: IconProps) {
    return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" className={className}>{paths}</svg>;
  };
}

export const Activity = icon(<path d="M2 12h5l3-8 4 16 3-8h5" />);
export const ArrowRight = icon(<path d="M4 12h15m-6-6 6 6-6 6" />);
export const Bell = icon(<path d="M6 9a6 6 0 0 1 12 0c0 5 3 6 3 8H3c0-2 3-3 3-8m4 11a2 2 0 0 0 4 0" />);
export const Check = icon(<path d="m5 12 4 4L19 6" />);
export const ChevronRight = icon(<path d="m9 5 7 7-7 7" />);
export const Download = icon(<path d="M12 3v12m-5-5 5 5 5-5M4 16v4h16v-4" />);
export const ImagePlus = icon(<><path d="M14 4H4v16h16V10m-5-5h7m-3.5-3.5v7M4 17l5-5 4 4 2-2 5 4" /><circle cx="8" cy="8" r="1" /></>);
export const Leaf = icon(<path d="M4 20c6-1 8-7 12-10M6 17C1 5 16 7 21 3c0 10-3 15-10 15-2 0-4-1-5-1Z" />);
export const LoaderCircle = icon(<path d="M12 3a9 9 0 1 1-9 9" />);
export const MessageCircle = icon(<path d="M21 11a9 9 0 0 1-12 9l-5 1 1-5a9 9 0 1 1 16-5Z" />);
export const NotebookText = icon(<path d="M5 3h14v18H5zM3 7h4m-4 5h4m-4 5h4m3-9h5m-5 4h5" />);
export const Pill = icon(<path d="m7 20 13-13a4 4 0 0 0-6-5L2 15a4 4 0 1 0 5 5Zm1-11 6 6" />);
export const ShieldCheck = icon(<path d="m12 22 2-1c5-3 6-6 6-13V5l-8-3-8 3v3c0 7 1 10 6 13Zm-4-10 3 3 5-5" />);
export const X = icon(<path d="m6 6 12 12M18 6 6 18" />);
