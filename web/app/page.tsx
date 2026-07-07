import { CompanionStart } from "./companion-client";

const modules = [
  {
    key: "companion",
    icon: "♥",
    title: "情感陪伴",
    description: "温暖倾听 · 心灵陪伴",
  },
  {
    key: "life",
    icon: "✓",
    title: "生活助手",
    description: "日程提醒 · 生活帮手",
  },
  {
    key: "work",
    icon: "▣",
    title: "工作伙伴",
    description: "高效协作 · 智能办公",
  },
] as const;

export default function Home() {
  return (
    <main className="shell">
      <section className="hero" aria-labelledby="welcome-title">
        <span className="mascot" aria-hidden="true">☁</span>
        <div>
          <p className="eyebrow">伴AI · M4</p>
          <h1 id="welcome-title">今天想从哪里开始？</h1>
          <p className="subtitle">懂陪伴，也能一起把事情做好。</p>
        </div>
      </section>

      <section className="moduleGrid" aria-label="功能模块">
        {modules.map((module) => (
          <article className={`moduleCard ${module.key}`} key={module.key}>
            <span className="moduleIcon" aria-hidden="true">{module.icon}</span>
            <h2>{module.title}</h2>
            <p>{module.description}</p>
            <a className="moduleAction" href="#companion-start">{module.key === "companion" ? "开始体验" : "登录后使用"}</a>
          </article>
        ))}
      </section>
      <CompanionStart />
    </main>
  );
}
