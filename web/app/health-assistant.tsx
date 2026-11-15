"use client";

/* eslint-disable @next/next/no-img-element -- Private uploaded images use authenticated blob URLs, with no image optimizer cache. */
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity, ArrowRight, Bell, Check, ChevronRight, Download, ImagePlus, Leaf, LoaderCircle, MessageCircle, NotebookText, Pill, ShieldCheck, X } from "./health-icons";
import { dateLabel, displayMetric, followHealthRun, healthClient, safeSource, safeTimezone, type HealthRequest } from "./health-client";
import "./health-assistant.css";

// The health agent's action payload and recorded facts vary by record type.
type Item = Record<string, any>;
const mascot = "/health-assistant/health-mascot-green.png";
const userNav = [
  ["daily", "每日健康", Leaf],
  ["chat", "健康对话", MessageCircle],
  ["episodes", "我的病程", NotebookText],
  ["medications", "我的用药", Pill],
  ["signals", "历史线索", Activity],
  ["privacy", "我的与隐私", ShieldCheck],
] as const;

function HealthPhoto({ request, id, alt, className }: { request: HealthRequest; id: string; alt: string; className?: string }) {
  const [photo, setPhoto] = useState<{ id: string; src?: string }>();
  useEffect(() => {
    const controller = new AbortController();
    let url: string | undefined;
    void request(`/attachments/${encodeURIComponent(id)}`, { signal: controller.signal })
      .then(response => response.blob())
      .then(blob => {
        if (controller.signal.aborted) return;
        url = URL.createObjectURL(blob);
        setPhoto({ id, src: url });
      }).catch(() => { if (!controller.signal.aborted) setPhoto({ id }); });
    return () => { controller.abort(); if (url) URL.revokeObjectURL(url); };
  }, [id, request]);
  return photo?.id === id && photo.src
    ? <img src={photo.src} alt={alt} className={className} />
    : <small>{photo?.id === id ? "图片暂时无法读取" : "正在读取图片…"}</small>;
}

function Button({children,onClick,primary=false,danger=false,disabled=false,type='button',...props}:React.ButtonHTMLAttributes<HTMLButtonElement>&{primary?:boolean;danger?:boolean}){return <button {...props} type={type} disabled={disabled} onClick={onClick} className={`button ${primary?'primary':''} ${danger?'danger':''} ${props.className||''}`}>{children}</button>}
function Panel({title,action,children,className=''}:{title?:string;action?:React.ReactNode;children:React.ReactNode;className?:string}){return <section className={`panel ${className}`}>{title&&<div className="panel-heading"><h2>{title}</h2>{action}</div>}{children}</section>}
function Badge({children,warn=false}:{children:React.ReactNode;warn?:boolean}){return <span className={`badge ${warn?'warn':''}`}>{children}</span>}
function Empty({title,children}:{title:string;children?:React.ReactNode}){return <div className="empty"><Leaf size={28}/><h3>{title}</h3><p>{children}</p></div>}
function Dialog({title,children,onClose}:{title:string;children:React.ReactNode;onClose:()=>void}){const ref=useRef<HTMLDialogElement>(null);useEffect(()=>{ref.current?.showModal();const d=ref.current;return()=>d?.close();},[]);return <dialog ref={ref} onCancel={event => { event.preventDefault(); onClose(); }} aria-label={title} onClick={e=>{if(e.target===ref.current)onClose();}}><div className="dialog-heading"><h2>{title}</h2><button className="icon-button" aria-label="关闭弹窗" onClick={onClose}><X/></button></div>{children}</dialog>}

export default function HealthAssistant({ token }: { token: string }) {
  const [route, setRoute] = useState("daily");
  const [toast, setToast] = useState("");
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(""), 6500);
    return () => clearTimeout(timer);
  }, [toast]);

  return <section className="healthAssistant" aria-label="个人健康空间">
    <nav className="healthNav" aria-label="健康功能">
      {userNav.map(([id, label, Icon]) =>
        <button type="button" key={id} aria-current={route === id ? "page" : undefined} onClick={() => setRoute(id)}><Icon size={17} /><span>{label}</span></button>
      )}
    </nav>
    <UserWorkspace token={token} route={route} navigate={setRoute} notify={setToast} />
    {toast && <div className="toast" role="status">{toast}</div>}
  </section>;
}

function UserWorkspace({token,route,navigate,notify}:{token:string;route:string;navigate:(s:string)=>void;notify:(s:string)=>void}){
  const [state,setState]=useState<Item|null>(null),[conversation,setConversation]=useState<Item|null>(null),[summary,setSummary]=useState<Item|null>(null),[status,setStatus]=useState<Item|null>(null),[signals,setSignals]=useState<Item[]>([]),[loadError,setLoadError]=useState('');
  const [draft,setDraft]=useState(''),[files,setFiles]=useState<File[]>([]),[busy,setBusy]=useState(false),[running,setRunning]=useState<string|null>(null),[partial,setPartial]=useState(''),[stage,setStage]=useState(''),[largeImage,setLargeImage]=useState<string|null>(null),[saving,setSaving]=useState<string|null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const pendingSend=useRef<{text:string;files:File[];ids:string[];clientId:string}|null>(null);
  const bottom=useRef<HTMLDivElement|null>(null);
  const {api, post, request} = useMemo(() => healthClient(token), [token]);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    const [s,c,d,st,sg] = await Promise.all([
      api<Item>("/state", {signal}), api<Item>("/conversation", {signal}),
      api<Item>("/daily-summary", {signal}), api<Item>("/system/status", {signal}), api<{items:Item[]}>("/signals", {signal}),
    ]);
    if (signal?.aborted) return c;
    setLoadError("");setState(s);setConversation(c);setSummary(d);setStatus(st);setSignals(sg.items);
    return c;
  }, [api]);

  useEffect(() => {
    const controller = new AbortController();
    void post("/connect", {}, controller.signal).then(() => refresh(controller.signal)).then(c => {
      if (controller.signal.aborted) return;
      const run = c.runs.find((r:Item) => ["queued", "running"].includes(r.status));
      setRunning(run?.id ?? null);
    }).catch(error => {
      if (!controller.signal.aborted) setLoadError(error.message);
    });
    return () => controller.abort();
  }, [post, refresh, attempt]);

  useEffect(() => {
    if (!running) return;
    const controller = new AbortController();
    let reply = "";
    void followHealthRun(request, running, controller.signal, (event, value) => {
      if (event === "delta" && typeof value.text === "string") {
        reply += value.text;
        setPartial(reply);
      }
      if (event === "status") setStage("正在整理你的描述");
      if (event === "node") setStage("正在核对记录与参考资料");
      if (event === "error" && typeof value.message === "string") notify(value.message);
    }).then(() => refresh(controller.signal)).then(() => {
      if (!controller.signal.aborted) { setRunning(null); setPartial(""); setStage(""); }
    }).catch(error => {
      if (!controller.signal.aborted) {
        setRunning(null); setPartial(""); setStage("");
        notify(error.message);
      }
    });
    return () => controller.abort();
  }, [notify, refresh, request, running]);

  useEffect(()=>{if(route==='chat')bottom.current?.scrollIntoView({behavior:'smooth',block:'end'})},[conversation,partial,route]);

  function attachRun(id: string) {
    setPartial(""); setStage("正在整理你的描述"); setRunning(id);
  }

  async function exportRecords() {
    try {
      const data = await (await request("/export")).blob();
      const url = URL.createObjectURL(data);
      const link = document.createElement("a");
      link.href = url;
      link.download = "health-records.json";
      link.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (error) { notify((error as Error).message); }
  }

  async function clearRecords() {
    setSaving("clear");
    try {
      await post("/records/clear", { confirmed: true });
      setConfirmClear(false); setRunning(null); setLargeImage(null); setDraft(""); setFiles([]); pendingSend.current = null;
      setState(null); setSummary(null); setConversation(null);
      setAttempt(current => current + 1);
      notify("健康记录已清除，附件与运行状态正在清理。");
    } catch (error) { notify((error as Error).message); }
    finally { setSaving(null); }
  }

  async function send(event?: React.FormEvent) {
    event?.preventDefault();
    if ((!draft.trim() && !files.length) || busy || running || saving || !conversation) return;
    setBusy(true);
    try {
      let outgoing = pendingSend.current;
      if (!outgoing || outgoing.text !== draft || outgoing.files.length !== files.length ||
          outgoing.files.some((file, index) => file !== files[index])) {
        outgoing = { text: draft, files: [...files], ids: [], clientId: crypto.randomUUID() };
        pendingSend.current = outgoing;
      }

      for (let index = outgoing.ids.length; index < files.length; index++) {
        const body = new FormData();
        body.set("file", files[index]);
        const { id } = await api<{ id: string }>("/attachments", { method: "POST", body });
        outgoing.ids.push(id);
      }

      const { run_id } = await post<{ run_id: string }>(`/conversations/${conversation.id}/messages`, {
        content: draft, attachments: outgoing.ids, client_id: outgoing.clientId,
      });
      pendingSend.current = null;
      setDraft("");
      setFiles([]);
      navigate("chat");
      try { await refresh(); } finally { attachRun(run_id); }
    } catch (error) {
      notify((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function retryRun(run: Item) {
    if (busy || running || saving) return;
    setBusy(true);
    try {
      const { run_id } = await post<{ run_id: string }>(`/runs/${run.id}/retry`);
      try { await refresh(); } finally { attachRun(run_id); }
    } catch (error) {
      notify((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  function chooseFiles(next: File[]) {
    if (files.length + next.length > 4) {
      notify("每条消息最多添加4张图片。");
      return;
    }
    if (next.some(file => file.size > 8 * 1024 * 1024 || !["image/jpeg", "image/png", "image/webp"].includes(file.type))) {
      notify("请选择8MB以内的JPEG、PNG或WebP图片。");
      return;
    }
    setFiles([...files, ...next]);
  }

  async function confirm(action: Item, reject = false) {
    setSaving(action.id);
    try {
      await post(`/actions/${action.id}/${reject ? "reject" : "confirm"}`);
      if (!reject && action.kind === "delete_records") {
        pendingSend.current = null;
        setDraft("");
        setFiles([]);
        setLargeImage(null);
      }
      notify(reject ? "这次操作没有保存。" : action.kind === "delete_records"
        ? "健康记录已清除，附件与运行状态正在清理。" : "已确认并保存。");
      await refresh();
    } catch (error) {
      notify((error as Error).message);
    } finally {
      setSaving(null);
    }
  }

  function talk(content = "") {
    setDraft(content);
    navigate("chat");
  }

  const composer=<form className="composer" onSubmit={send}><div className="pending-images">{files.map((file,index)=><PendingImage key={file.name+index} file={file} disabled={busy} remove={()=>setFiles(files.filter((_,i)=>i!==index))}/>)}</div><label className="sr-only" htmlFor="health-message">描述你的情况</label><textarea id="health-message" disabled={busy} value={draft} onChange={e=>setDraft(e.target.value)} placeholder="慢慢说，今天感觉怎么样？也可以附上照片…" maxLength={12000} onKeyDown={e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.nativeEvent.isComposing){e.preventDefault();send();}}}/><div className="composer-tools"><label className="image-picker"><ImagePlus size={18}/>添加图片<input type="file" disabled={busy} accept="image/jpeg,image/png,image/webp" multiple onChange={e=>{chooseFiles(Array.from(e.target.files||[]));e.target.value='';}}/></label><small>{status?.model_configured ? "发送后，健康模型会处理消息、图片和相关记录。" : "用于日常健康记录。"}</small>{running?<Button onClick={async()=>{try{await post(`/runs/${running}/cancel`)}catch(e){notify((e as Error).message)}}}>停止</Button>:<Button primary type="submit" disabled={busy||(!draft.trim()&&!files.length)}>{busy?<LoaderCircle className="spin" size={15}/>:<ArrowRight size={16}/>}发送</Button>}</div></form>;
  if(loadError)return <Panel title="暂时无法读取记录"><p>{loadError}</p><Button onClick={()=>setAttempt(current => current + 1)}>重新读取</Button></Panel>;
  if(!state||!summary||saving||!conversation)return <div className="loading-screen"><LoaderCircle className="spin"/>正在读取你的记录…</div>;
  const active=state.episodes.filter((e:Item)=>e.status==='active');const archived=state.episodes.filter((e:Item)=>e.status==='archived');const pending=new Map<string,Item>(state.pending_actions.map((a:Item)=>[a.id,a]));
  const info=status?.test_provider?<div className="service-banner warn">自动验收模式 · 测试输出不构成医学建议</div>:!status?.model_configured?<div className="service-banner">健康对话暂未连接AI服务。消息可以保存，已有记录仍可查看。</div>:null;
  return <>{info}<header className="workspace-heading"><div><small>{new Intl.DateTimeFormat('zh-CN',{dateStyle:'full',timeZone:safeTimezone(state.profile[0]?.data?.timezone)}).format(new Date())}</small><h1>{route==='privacy'?'我的与隐私':userNav.find(n=>n[0]===route)?.[1]||'每日健康'}</h1></div>{route!=='chat'&&<Button onClick={()=>talk()}><MessageCircle size={16}/>和伙伴聊聊</Button>}</header>
    {route==='daily'&&<>{state.tasks.some((t:Item)=>t.status==='due')&&<Panel title="今天的待办">{state.tasks.filter((t:Item)=>t.status==='due').map((t:Item)=><div className="list-row" key={t.id}><h3>{t.title}</h3><Button onClick={()=>talk(`完成提醒“${t.title}”，记录这次情况`)}>和伙伴记录</Button></div>)}</Panel>}<section className="welcome-card"><div className="welcome-copy"><Badge>{active.length?'正在一起跟进':'每天一点，慢慢了解自己'}</Badge><h2>{active.length?'今天，有没有一点新变化？':'今天，哪里有点不舒服？'}</h2><p>{active.length?'说说今天的感受，我会帮你整理成连续记录。':'有什么不适或健康疑问，都可以慢慢告诉我。'}</p><img src={mascot} alt="健康伙伴陪你记录日常变化"/></div>{composer}</section><div className="health-grid"><Panel title="今日关照" action={<Badge>{active.length?'跟进中':archived.length?'康复与日常':'日常关照'}</Badge>}><ul className="care-list">{summary.care.map((s:string)=><li key={s}>{s}</li>)}</ul>{state.profile[0]?.data?.city&&<p className="muted">已记录城市：{state.profile[0].data.city}。环境建议将结合你提供的个人情况；当前没有实时天气数据。</p>}<button className="text-button" onClick={()=>talk('我想补充我的个人健康背景')}>补充个人情况<ArrowRight size={14}/></button></Panel><Panel title={active.length?'当前观察':'历史健康观察'}><div className="summary-pair"><div><small>{active.length?'正在跟进':'已结束记录'}</small><strong>{active.length||archived.length}<span>段</span></strong></div><div><small>已记录观察</small><strong>{state.observations.length}<span>次</span></strong></div></div><p className="muted">{active.length?'趋势只使用你实际提供的记录；没有填写的日期保留空缺。':archived.length?'历史记录用于回顾，不代表当前仍有这些不适，也不会自动续药。':'还没有个人历史。以后出现小变化时，可以从一段对话开始。'}</p>{summary.insights?.map((i:Item)=><div className="daily-insight" key={i.episode_id}><h3>{i.complaint}</h3><p>{i.message}</p></div>)}<button className="text-button" onClick={()=>navigate('episodes')}>查看记录<ArrowRight size={14}/></button></Panel></div>{active.length>0&&<Panel title="正在跟进">{active.map((e:Item)=><EpisodeCard key={e.id} episode={e} request={request} messages={conversation.messages} observations={state.observations.filter((o:Item)=>o.episode_id===e.id)} onTalk={talk}/>)}</Panel>}{!active.length&&archived.length>0&&<Panel title="最近结束的记录">{archived.slice(0,2).map((e:Item)=><div className="list-row" key={e.id}><div><h3>{e.complaint}</h3><Badge>已结束 · 非当前不适</Badge></div><button className="text-button" onClick={()=>navigate('episodes')}>回顾<ChevronRight size={15}/></button></div>)}</Panel>}</>}
    {route==='chat'&&<section className="chat-panel"><div className="chat-heading"><img src={mascot} alt=""/><div><strong>健康伙伴在这里</strong><small>说说感受，也可以发来照片</small></div><Badge>{running?'正在整理':'图文对话'}</Badge></div><div className="chat-body" role="log" aria-label="健康对话记录" aria-live="polite">{conversation.messages.length===0?<div className="chat-welcome"><img src={mascot} alt=""/><h2>今天有什么不舒服？</h2><p>不着急，按你的方式说。<br/>我会陪你把情况一点点整理清楚。</p></div>:conversation.messages.map((m:Item)=><article className={`message ${m.role}`} key={m.id}>{m.role==='assistant'&&<img className="message-avatar" src={mascot} alt=""/>}<div className="message-content"><small>{m.role==='assistant'?'健康伙伴':'我'} · {dateLabel(m.created_at)}</small><div className="message-bubble"><p>{m.content}</p>{m.attachments?.length>0&&<div className="message-images">{m.attachments.map((id:string,i:number)=><button key={id} onClick={()=>setLargeImage(id)} aria-label={`放大第${i+1}张图片`}><HealthPhoto key={id} request={request} id={id} alt={`用户发送的第${i+1}张图片`} /></button>)}</div>}</div>{m.role==='user'&&<RunFeedback run={conversation.runs.find((r:Item)=>r.message_id===m.id)} busy={busy||!!running} retry={retryRun}/>} {m.metadata?.actions?.map((a:Item)=><ActionCard key={a.id} action={pending.get(a.id)||{...a,status:'resolved'}} busy={!!saving} confirm={reject=>confirm(a,reject)}/>)}{m.metadata?.sources?.length>0&&<details className="sources"><summary>参考资料</summary>{m.metadata.sources.map((s:Item)=><a key={s.id} href={safeSource(s.source_url)} target="_blank" rel="noopener noreferrer">{s.title}<small>资料版本 {s.version}</small></a>)}</details>}</div></article>)}{running&&<article className="message assistant"><img className="message-avatar" src={mascot} alt=""/><div className="message-content"><small><LoaderCircle size={12} className="spin"/>{stage}</small><div className="message-bubble"><p>{partial||'我在整理你刚刚说的情况…'}</p></div></div></article>}<div ref={bottom}/></div>{composer}</section>}
    {route==='episodes'&&<div className="stack">{state.episodes.length?state.episodes.map((e:Item)=><EpisodeCard key={e.id} episode={e} request={request} messages={conversation.messages} observations={state.observations.filter((o:Item)=>o.episode_id===e.id)} onTalk={talk}/>):<Panel><Empty title="还没有病程记录">有不适时直接告诉我，我们从你的描述开始。</Empty></Panel>}</div>}
    {route==='medications'&&<>{state.medication_checks?.length>0&&<Panel title="值得核对的信息">{state.medication_checks.map((check:Item)=><div className="signal" key={check.id}><Badge warn>待核对</Badge><h3>{check.title}</h3><p>{check.facts.join(' · ')}</p><small>{check.message}</small>{check.source&&<a className="text-button" href={safeSource(check.source.source_url)} target="_blank" rel="noopener noreferrer">说明依据 · {check.source.version}</a>}<button className="text-button" onClick={()=>talk(`一起核对这条用药信息：${check.title}`)}>和伙伴核对<ArrowRight size={14}/></button></div>)}</Panel>}<Panel title="实际用药记录" action={<Button onClick={()=>talk('我想记录今天实际使用的药品')}>通过对话记录</Button>}>{state.medications.length?state.medications.map((m:Item)=><div className="medication-card" key={m.id}><div className="list-row"><h3>{m.data.name}</h3><Badge>{({used:'已使用',not_used:'明确未用',unknown:'记不清'} as Item)[m.data.state]||'待核对'}</Badge></div><dl className="facts"><div><dt>已记录成分</dt><dd>{m.data.ingredients?.join('、')||'尚未确认'}</dd></div><div><dt>实际用量与频次</dt><dd>{[m.data.amount,m.data.frequency].filter(Boolean).join(' · ')||'尚未补充'}</dd></div><div><dt>记录时间</dt><dd>{dateLabel(m.recorded_at)}</dd></div></dl>{m.data.note&&<p className="muted">{m.data.note}</p>}{state.product_information?.filter((p:Item)=>p.record_id===m.id).map((p:Item)=><ProductInformation key={p.id+p.version} product={p}/>)}<button className="text-button" onClick={()=>talk(`帮我核对${m.data.name}的成分、说明和实际使用记录`)}>核对使用情况<ArrowRight size={14}/></button><button className="text-button" onClick={()=>talk(`更正用药记录“${m.data.name}”，请先和我核对要修改的内容`)}>更正记录</button></div>):<Empty title="先从你实际使用的产品开始">告诉我产品名称、包装说明与实际使用情况，也可以附药盒照片。</Empty>}</Panel></>}
    {route==='signals'&&<Panel title="历史中的变化" action={<Button onClick={()=>talk('帮我根据历史记录和知识资料核对值得进一步了解的变化')}>和伙伴一起回顾</Button>}>{signals.length?signals.map(s=><div className="signal" key={s.id}><Badge warn>值得进一步核实</Badge><h3>{s.title}</h3><p>{s.message}</p><ul>{s.evidence.map((e:Item)=><li key={e.id}>{e.label} · {e.date}</li>)}</ul><a href={safeSource(s.source_url)} target="_blank" rel="noopener noreferrer">查看知识来源</a></div>):<Empty title="暂时没有形成需要提示的历史组合">这里只根据实际记录匹配有限知识资料；没有提示不能排除疾病。</Empty>}</Panel>}
    {route==='privacy'&&<div className="health-grid"><Panel title="我的健康背景"><dl className="facts">{[['年龄','age'],['年龄段','age_range'],['城市','city'],['地区','region'],['孕哺情况','pregnancy_status'],['过敏','allergies'],['相关病史','conditions'],['在用药','current_medications']].map(([label,key])=><div key={key}><dt>{label}</dt><dd>{Array.isArray(state.profile[0]?.data?.[key])?state.profile[0].data[key].join('、')||'明确没有':valueLabel(state.profile[0]?.data?.[key]??'未补充',key)}</dd></div>)}</dl><Button onClick={()=>talk('我想补充或更正我的个人健康背景')}>通过对话补充</Button></Panel><Panel title="数据由你决定"><p className="muted">此空间仅用于管理你的个人健康记录，未开放研究访问。开启图文对话后，健康模型会处理你的消息、图片及相关记录。</p><div className="button-row"><Button onClick={() => void exportRecords()}><Download size={15}/>导出我的记录</Button><Button danger disabled={busy || !!saving} onClick={() => setConfirmClear(true)}>清除健康记录</Button></div></Panel><Panel title="提醒与待办"><TaskList items={state.tasks} talk={talk}/></Panel></div>}
    <footer className="page-footer"><ShieldCheck size={13}/>用于日常记录和护理参考，不提供诊断或处方</footer>{largeImage&&<Dialog title="查看图片" onClose={()=>setLargeImage(null)}><HealthPhoto key={largeImage} request={request} id={largeImage} className="large-image" alt="用户发送的图片大图"/></Dialog>}{confirmClear && <Dialog title="清除健康记录" onClose={() => { if (!saving) setConfirmClear(false); }}>
      <p>这会清除健康对话、病程、用药、提醒、图片及个人健康背景。伴 AI 账户仍可继续使用。备份会按部署保留期清理，已导出的副本不会自动删除。</p>
      <div className="button-row"><Button disabled={!!saving} onClick={() => setConfirmClear(false)}>取消</Button><Button danger disabled={!!saving} onClick={() => void clearRecords()}>{saving ? "正在清除…" : "确认清除"}</Button></div>
    </Dialog>}</>
}

function ProductInformation({product:p}:{product:Item}){return <details className="product-leaflet"><summary>匹配的产品说明 · {p.version}</summary>{p.data_origin==='synthetic'&&<Badge warn>合成说明 · 仅用于验收</Badge>}<dl className="facts"><div><dt>地区与规格</dt><dd>{p.region} · {p.strength} · {p.form}</dd></div><div><dt>来源成分</dt><dd>{p.ingredients.join('、')}</dd></div><div><dt>途径</dt><dd>{p.route}</dd></div><div><dt>版本有效期</dt><dd>{p.valid_until.slice(0,10)}（UTC）</dd></div></dl><p>{p.information}</p>{p.warnings?.length>0&&<ul>{p.warnings.map((w:string)=><li key={w}>{w}</li>)}</ul>}{p.dosage_available?<div className="source-box"><strong>说明中的原文信息</strong><p>{p.dosage_label}</p><small>用于核对该产品说明，不自动生成个体处方或续方。</small></div>:<p className="notice">{p.missing_background?.length?`尚需核对：${p.missing_background.join('、')}。`:'该版本未发布用量内容。'}当前不显示用量说明。</p>}<a className="text-button" href={safeSource(p.source_url)} target="_blank" rel="noopener noreferrer">查看原始说明</a></details>}
function RunFeedback({run,busy,retry}:{run?:Item;busy:boolean;retry:(r:Item)=>void}){if(!run||!['failed','cancelled'].includes(run.status))return null;const errors:Item={provider_not_configured:'AI服务尚未配置。配置后可重试，原消息与图片已保留。',agent_unavailable:'伙伴暂时没有连接上，原消息与图片已保留。',model_timeout:'这次等待超时了，原消息与图片已保留。',unsupported_dosage_advice:'这次回答包含尚未核验的用量信息，未作为建议显示。'};return <div className="run-feedback" role="status"><p>{run.status==='cancelled'?'这次处理已停止，原消息与图片已保留。':errors[run.error_code]||'这次没有处理完成，原消息与图片已保留。'}</p><Button disabled={busy} onClick={()=>retry(run)}>重新处理这条消息</Button></div>}
function PendingImage({ file, remove, disabled = false }: { file: File; remove: () => void; disabled?: boolean }) {
  const img = useRef<HTMLImageElement>(null);
  useEffect(() => {
    const url = URL.createObjectURL(file);
    if (img.current) img.current.src = url;
    return () => URL.revokeObjectURL(url);
  }, [file]);
  return <div><img ref={img} alt={file.name} /><button disabled={disabled} aria-label={`移除图片${file.name}`} onClick={remove} type="button"><X size={14} /></button><small>{file.name}</small></div>;
}
const actionLabels:Item={create_episode:'整理这次不适',checkin:'记录今天的变化',update_medication:'更正实际用药',record_medication:'记录实际用药',archive_episode:'结束本次跟进',update_profile:'更新个人健康背景',create_task:'设置提醒',update_task:'更新提醒',delete_records:'清除健康记录'};
const fieldLabels:Item={complaint:'你的描述',topic:'整理主题',started_at:'开始时间',observed_date:'观察日期',score:'不适自评分',medication_state:'用药状态',activity:'活动影响',note:'补充说明',symptoms:'症状记录',region:'使用地区',form:'剂型',leaflet_version:'说明版本',name:'产品名称',ingredients:'已确认成分',strength:'规格与强度',route:'使用途径',amount:'实际用量',frequency:'实际频次',used_at:'实际使用时间',state:'使用状态',basis:'使用依据',reason:'结束原因',age:'年龄',age_range:'年龄段',allergies:'过敏',conditions:'相关病史',current_medications:'在用药',city:'城市',timezone:'时区',pregnancy_status:'孕哺情况',title:'提醒内容',due_at:'提醒时间',recurrence:'重复方式',status:'提醒状态',scope:'删除范围'};
function valueLabel(value:unknown,field?:string){if(value===true)return '同意';if(value===false)return '不同意或撤回';if(value===null)return '未确定';if(Array.isArray(value))return value.map(v=>v && typeof v==='object'?`${({thirst:'持续口渴',urination:'排尿增多',weight_loss:'非主动体重下降'} as Item)[v.code]||v.code||''}：${({present:'存在',absent:'否认',unknown:'未确定'} as Item)[v.assertion]||v.assertion||''}`:v).join('、')||(['allergies','conditions','current_medications'].includes(field||'')?'明确没有':'尚未确认');if(typeof value==='object')return JSON.stringify(value);return ({not_pregnant:'未孕，哺乳情况待补充',neither_pregnant_nor_breastfeeding:'未孕且未哺乳（自述）',not_applicable:'不适用（请核对）',health:'病程、聊天、用药、提醒、图片与相关衍生记录',used:'已使用',not_used:'明确未用',unknown:'记不清',daily:'每天',once:'一次',paused:'暂停',completed:'完成',scheduled:'计划中'} as Item)[String(value)]||String(value)}
function ActionCard({action,busy,confirm}:{action:Item;busy:boolean;confirm:(reject?:boolean)=>void}){return <div className="action-card"><small>请核对后确认</small><h3>{actionLabels[action.kind]||'待确认操作'}</h3>{action.kind==='delete_records'&&<p className="notice">确认后清除当前健康记录，后台会取消相关运行、清理图片与检查点。账户及最小审计保留；备份按部署保留期清理（默认7天），已下载的外部副本无法自动收回。</p>}<dl className="facts">{Object.entries(action.payload).filter(([k])=>fieldLabels[k]).map(([k,v])=><div key={k}><dt>{fieldLabels[k]}</dt><dd>{valueLabel(v,k)}</dd></div>)}</dl>{action.status==='pending'?<div className="button-row"><Button primary={action.kind!=='delete_records'} danger={action.kind==='delete_records'} disabled={busy} onClick={()=>confirm()}>{busy?<LoaderCircle className="spin" size={14}/>:<Check size={14}/>}确认{action.kind==='delete_records'?'删除健康记录':'保存'}</Button><Button disabled={busy} onClick={()=>confirm(true)}>不保存</Button></div>:<Badge>已处理</Badge>}</div>}
function EpisodeCard({episode,observations,messages=[],onTalk,request}:{episode:Item;request:HealthRequest;observations:Item[];messages?:Item[];onTalk:(s:string)=>void}){const sorted=[...observations].sort((a,b)=>a.observed_date.localeCompare(b.observed_date));return <Panel className="episode-card"><div className="panel-heading"><div><h2>{episode.complaint}</h2><small>{dateLabel(episode.started_at)} · {episode.onset_known?'自述开始时间':'记录起点，症状开始时间待补充'}</small></div><Badge>{episode.status==='active'?'跟进中':'已结束 · 非当前不适'}</Badge></div><div className="episode-content"><div><h3>你实际记录的变化</h3>{sorted.length?<div className="score-observations" role="img" aria-label="实际自评分记录，没有填写的评分不补零">{sorted.map(o=><div key={o.id}><div className="score-track">{typeof o.data.score==='number'?<i style={{height:`${o.data.score*7+3}px`}}/>:<span>—</span>}</div><strong>{displayMetric(o.data.score)}</strong><small>{o.observed_date.slice(5,10)}</small></div>)}</div>:<p className="muted">还没有每日观察。描述今天的变化后，再逐步形成时间记录。</p>}</div><div className="timeline">{sorted.slice(-5).map(o=><article key={o.id}><small>{o.observed_date.slice(0,10)} · 记录版本{o.version}</small><p>{o.data.note||'已补充当天观察'}</p><small>用药：{valueLabel(o.data.medication_state||'尚未记录')}</small>{messages.find((m:Item)=>m.id===o.source_message_id)?.attachments?.map((id:string)=><HealthPhoto key={id} request={request} id={id} className="observation-photo" alt="这次观察关联的照片"/>)}</article>)}</div></div>{episode.status==='active'&&<div className="button-row"><Button onClick={()=>onTalk(`记录一下“${episode.complaint}”今天的变化`)}>说说今天的情况</Button><button className="text-button" onClick={()=>onTalk(`结束“${episode.complaint}”的这次跟进`)}>结束跟进</button></div>}</Panel>}
function TaskList({items,talk}:{items:Item[];talk:(s:string)=>void}){return <>{items.length?items.map(t=><div className="list-row" key={t.id}><div><h3>{t.title}</h3><small>{dateLabel(t.due_at)} · {valueLabel(t.status)}</small></div><button className="text-button" onClick={()=>talk(`调整或暂停提醒“${t.title}”`)}>调整</button></div>):<p className="muted">你可以说“每天晚上提醒我记录今天的变化”。</p>}<Button onClick={()=>talk('我想设置一个随访提醒') }><Bell size={15}/>通过对话设置</Button></>}
