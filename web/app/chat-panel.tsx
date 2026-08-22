"use client";

import { FormEvent, useEffect, useRef, useState } from "react";
import { confirmationPrompt, executionResultNotice, visibleChatText } from "./chat-content";
import { isActiveRun, listSkillRuns } from "./skill-run-client";

const apiBase = process.env.NEXT_PUBLIC_API_BASE_URL ?? "http://localhost:8080";
type Message = { id: string; role: "user" | "assistant"; sequence: number; bubble: number; content: string; status: string; reply_to_id?: string };
type ToolConfirmationEvent = { tool: string; confirmation?: { kind: string; candidate_id: string; summary: string; payload?: Record<string,string> } };
type PendingDocument = { id: string; name: string; media_type: string; size_bytes: number };
type GeneratedFileLink = { runID: string; fileID: string; name: string };
type LedgerExportLink = { exportID: string; name: string };
type AgentInterrupt = { type: string; summary: string; tool_name?: string; arguments?: Record<string,unknown> };
type AgentRunOutput = { interrupts?: AgentInterrupt[]; tool_result?: { response?: string } };
type AgentRun = { id: string; status: string; revision: number; output?: AgentRunOutput; error_message?: string };

export function ChatPanel({ token, character, onClose }: { token: string; character: { id: string; name: string; module: "companion" | "life" | "work" }; onClose: () => void }) {
  const [conversationID, setConversationID] = useState(""); const [messages, setMessages] = useState<Message[]>([]); const [jobID, setJobID] = useState(""); const [agentRunID, setAgentRunID] = useState(""); const [status, setStatus] = useState(""); const [error, setError] = useState(""); const [toolNotice, setToolNotice] = useState(""); const [pendingDocuments, setPendingDocuments] = useState<PendingDocument[]>([]); const [uploading, setUploading] = useState(false); const bottomRef = useRef<HTMLDivElement>(null); const handledConfirmationsRef = useRef(new Set<string>()); const handledAgentResolutionsRef = useRef(new Set<string>()); const handledRetryDeliveriesRef = useRef(new Set<string>());
  useEffect(() => { let active = true; void ensureConversation(token, character.id).then(async (id) => { if (!active) return; setConversationID(id); const items = await loadMessages(token,id); if(active)setMessages(items); }).catch((cause:Error)=>setError(cause.message)); return()=>{active=false}; },[token,character.id]);
  useEffect(()=>{bottomRef.current?.scrollIntoView({behavior:"smooth"})},[messages,status]);
  useEffect(()=>{if(character.module!=="work"||!conversationID)return;let active=true;handledRetryDeliveriesRef.current.clear();const sync=async()=>{try{const items=await listSkillRuns(token,{limit:20,conversationID});if(!active)return;const pendingDeliveries=items.filter(run=>run.attempt>1&&!isActiveRun(run)).map(run=>`${run.id}:${run.attempt}:${run.status}`).filter(key=>!handledRetryDeliveriesRef.current.has(key));if(pendingDeliveries.length>0){const latest=await loadMessages(token,conversationID);if(active){pendingDeliveries.forEach(key=>handledRetryDeliveriesRef.current.add(key));setMessages(current=>mergeMessages(current,latest))}}}catch{}};void sync();const timer=window.setInterval(()=>void sync(),2000);const refresh=()=>void sync();window.addEventListener("ai-companion-skill-runs-updated",refresh);return()=>{active=false;window.clearInterval(timer);window.removeEventListener("ai-companion-skill-runs-updated",refresh)}},[token,character.module,conversationID]);

  async function send(event: FormEvent<HTMLFormElement>) { event.preventDefault(); const form=event.currentTarget;const data=new FormData(form);const content=String(data.get("content")??"").trim();if((!content&&pendingDocuments.length===0)||!conversationID||jobID||agentRunID||uploading)return;setError("");setToolNotice("");setStatus("正在回应…");
    try{const response=await fetch(`${apiBase}/v1/conversations/${conversationID}/messages`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({content,document_ids:pendingDocuments.map(item=>item.id)})});const payload=await response.json() as {message?:Message|string;job?:{id:string};agent_run?:AgentRun};if(!response.ok||!payload.message||typeof payload.message==="string")throw new Error(typeof payload.message==="string"?payload.message:"发送失败");const userMessage=payload.message;form.reset();setPendingDocuments([]);setMessages(current=>mergeMessage(current,userMessage));if(payload.agent_run){setAgentRunID(payload.agent_run.id);await consumeAgentRun(payload.agent_run.id)}else if(payload.job){setJobID(payload.job.id);await consumeEvents(payload.job.id)}else{throw new Error("服务端未返回可执行任务")}}
    catch(cause){setError(cause instanceof Error?cause.message:"发送失败");setStatus("生成失败，可重试");} }

  async function uploadDocuments(files:FileList|null){if(character.module!=="work"||!files?.length)return;const remaining=Math.max(0,3-pendingDocuments.length);if(remaining===0){setError("单条消息最多发送 3 个文件");return}setUploading(true);setError("");try{const uploaded:PendingDocument[]=[];for(const file of Array.from(files).slice(0,remaining)){const body=new FormData();body.append("file",file);const response=await fetch(`${apiBase}/v1/documents`,{method:"POST",headers:{Authorization:`Bearer ${token}`},body});const payload=await response.json() as {document?:PendingDocument;message?:string};if(!response.ok||!payload.document)throw new Error(payload.message??`无法上传 ${file.name}`);uploaded.push(payload.document)}setPendingDocuments(current=>[...current,...uploaded])}catch(cause){setError(cause instanceof Error?cause.message:"文件上传失败")}finally{setUploading(false)}}

  async function downloadGeneratedFile(file:GeneratedFileLink){const response=await fetch(`${apiBase}/v1/skill-runs/${file.runID}/files/${file.fileID}`,{headers:{Authorization:`Bearer ${token}`}});if(!response.ok){setError("文件下载失败");return}const blob=await response.blob();const url=URL.createObjectURL(blob);const anchor=document.createElement("a");anchor.href=url;anchor.download=file.name;document.body.appendChild(anchor);anchor.click();anchor.remove();URL.revokeObjectURL(url)}
  async function downloadLedgerExport(file:LedgerExportLink){const response=await fetch(`${apiBase}/v1/ledger/exports/${file.exportID}/file`,{headers:{Authorization:`Bearer ${token}`}});if(!response.ok){setError("账单文件下载失败");return}const blob=await response.blob();const url=URL.createObjectURL(blob);const anchor=document.createElement("a");anchor.href=url;anchor.download=file.name;document.body.appendChild(anchor);anchor.click();anchor.remove();URL.revokeObjectURL(url)}
  async function confirmTool(payload:ToolConfirmationEvent){const confirmation=payload.confirmation;if(!confirmation||handledConfirmationsRef.current.has(confirmation.candidate_id))return;if(!["ledger","reminder","today_plan","reminder_reschedule","today_plan_schedule","task_complete"].includes(confirmation.kind))return;handledConfirmationsRef.current.add(confirmation.candidate_id);const approved=window.confirm(confirmationPrompt(confirmation.summary));if(!approved){setToolNotice("已取消，本次更新未写入。");return}
    try{let response:Response;
      if(confirmation.kind==="ledger"){response=await fetch(`${apiBase}/v1/ledger/candidates/${confirmation.candidate_id}/confirm`,{method:"POST",headers:{"Content-Type":"application/json","Idempotency-Key":`chat-ledger-confirm:${confirmation.candidate_id}`,Authorization:`Bearer ${token}`},body:JSON.stringify({note:"由角色聊天确认写入"})})}
      else if(confirmation.kind==="reminder"){response=await fetch(`${apiBase}/v1/reminders/${confirmation.candidate_id}/confirm`,{method:"POST",headers:{"Content-Type":"application/json","Idempotency-Key":`chat-reminder-confirm:${confirmation.candidate_id}`,Authorization:`Bearer ${token}`},body:"{}"})}
      else if(confirmation.kind==="reminder_reschedule"){const update=confirmation.payload??{};response=await fetch(`${apiBase}/v1/reminders/${update.reminder_id}/reschedule`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({local_due:update.local_due,timezone:update.timezone,expected_updated_at:update.expected_updated_at})})}
      else if(confirmation.kind==="today_plan_schedule"){const update=confirmation.payload??{};response=await fetch(`${apiBase}/v1/plans/today/items/${update.item_id}/schedule`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({starts_at:update.starts_at,expected_updated_at:update.expected_updated_at})})}
      else if(confirmation.kind==="task_complete"){const update=confirmation.payload??{};const path=update.task_type==="reminder"?`/v1/reminders/${update.item_id}/complete`:`/v1/plans/today/items/${update.item_id}/complete`;response=await fetch(`${apiBase}${path}`,{method:"POST",headers:{Authorization:`Bearer ${token}`}})}
      else{const plan=confirmation.payload??{};response=await fetch(`${apiBase}/v1/plans/today/items`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({title:plan.title,local_date:plan.local_date,timezone:plan.timezone,source:plan.source})})}
      if(!response.ok){const body=await response.json().catch(()=>({message:"更新失败"})) as {message?:string};throw new Error(body.message??"更新失败")}
      if(["reminder","today_plan","reminder_reschedule","today_plan_schedule","task_complete"].includes(confirmation.kind))window.dispatchEvent(new Event("ai-companion-life-updated"));
      setToolNotice(confirmation.kind==="ledger"?"账单已确认并保存。":confirmation.kind==="reminder"?"提醒已确认并创建。":confirmation.kind==="reminder_reschedule"?"提醒时间已确认并更新。":confirmation.kind==="today_plan_schedule"?"今日计划时间已确认并更新。":confirmation.kind==="task_complete"?"事项已标记为完成。":"事项已确认并加入今日计划。")
    }catch(error){handledConfirmationsRef.current.delete(confirmation.candidate_id);throw error}}

  async function applyTerminalStatus(id:string,jobStatus:string){if(jobStatus==="completed"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setStatus("");setJobID("");return true}if(jobStatus==="cancelled"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setStatus("已停止生成");setJobID("");return true}if(jobStatus==="failed"||jobStatus==="timed_out"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setStatus(jobStatus==="timed_out"?"处理超时，可重试":"生成失败，可重试");setJobID(id);return true}return false}

  async function consumeEvents(id:string){let afterEventID=0;let failures=0;let finished=false;
    while(!finished){try{const response=await fetch(`${apiBase}/v1/generation-jobs/${id}/events?after_event_id=${afterEventID}`,{headers:{Authorization:`Bearer ${token}`}});
      if(!response.ok)throw new Error("实时连接失败");const reader=response.body?.getReader();if(!reader)throw new Error("浏览器不支持流式响应");const decoder=new TextDecoder();let buffer="";failures=0;
      while(true){const {done,value}=await reader.read();buffer+=decoder.decode(value,{stream:!done});const frames=buffer.split("\n\n");buffer=frames.pop()??"";
        for(const frame of frames){const eventID=Number(frame.match(/^id: (\\d+)$/m)?.[1]??0);if(eventID>afterEventID)afterEventID=eventID;const type=frame.match(/^event: (.+)$/m)?.[1];const raw=frame.match(/^data: (.+)$/m)?.[1];if(type==="tool_confirmation_required"&&raw){await confirmTool(JSON.parse(raw) as ToolConfirmationEvent)}if(type==="bubble"&&raw){setMessages(current=>mergeMessage(current,JSON.parse(raw) as Message))}if(type&&await applyTerminalStatus(id,type)){finished=true;break}}if(done||finished)break}
      if(finished)break;const current=await loadGenerationJob(token,id);if(await applyTerminalStatus(id,current.status)){break}setStatus("任务仍在处理中，正在恢复连接…");await delay(400);
    }catch{failures++;try{const current=await loadGenerationJob(token,id);if(await applyTerminalStatus(id,current.status)){break}}catch{}if(failures>=5)throw new Error("与服务的连接中断，请稍后重试");setStatus("连接暂时中断，正在自动恢复…");await delay(Math.min(400*failures,1600))}}
  }

  async function consumeAgentRun(id:string){let failures=0;
    while(true){try{const response=await fetch(`${apiBase}/v1/agent-runs/${id}`,{headers:{Authorization:`Bearer ${token}`}});const run=await response.json() as AgentRun&{message?:string};if(!response.ok)throw new Error(run.message??"无法读取 Agent 状态");failures=0;
      if(run.status==="waiting_approval"){const interrupt=run.output?.interrupts?.[0];if(!interrupt)throw new Error("确认信息不完整，请稍后重试");const resolutionKey=`${id}:${run.revision}`;if(!handledAgentResolutionsRef.current.has(resolutionKey)){handledAgentResolutionsRef.current.add(resolutionKey);const approved=window.confirm(confirmationPrompt(interrupt.summary));const resolved=await fetch(`${apiBase}/v1/agent-runs/${id}/resolve`,{method:"POST",headers:{"Content-Type":"application/json","Idempotency-Key":resolutionKey,Authorization:`Bearer ${token}`},body:JSON.stringify({approved})});if(!resolved.ok){handledAgentResolutionsRef.current.delete(resolutionKey);const body=await resolved.json().catch(()=>({message:"提交确认失败"})) as {message?:string};throw new Error(body.message??"提交确认失败")}setToolNotice(approved?"已确认，正在执行更新…":"已取消，本次更新不会写入。")}setStatus("正在恢复任务…")}
      else if(run.status==="completed"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setStatus("");setAgentRunID("");setToolNotice(executionResultNotice(run.output?.tool_result?.response));return}
      else if(run.status==="cancelled"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setStatus("已停止生成");setAgentRunID("");setToolNotice("操作已取消，原对话已保留。");return}
      else if(run.status==="timed_out"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setError("这次处理超时，原对话已保留，可在对应消息下重试。");setStatus("");setAgentRunID("");return}
      else if(run.status==="failed"){const latest=await loadMessages(token,conversationID);setMessages(current=>mergeMessages(current,latest));setError("这次处理没有完成，原对话已保留，可在对应消息下重试。");setStatus("");setAgentRunID("");return}
      else{setStatus(run.status==="queued"?"任务已进入恢复队列…":run.status==="cancel_requested"?"正在停止…":"正在回应…")}
      await delay(400);
    }catch(cause){failures++;if(failures>=5){setAgentRunID("");throw cause}setStatus("连接暂时中断，正在自动恢复…");await delay(Math.min(400*failures,1600))}}
  }

  async function stop(){if(agentRunID){const response=await fetch(`${apiBase}/v1/agent-runs/${agentRunID}/cancel`,{method:"POST",headers:{"Idempotency-Key":`${agentRunID}:cancel`,Authorization:`Bearer ${token}`}});if(!response.ok){const body=await response.json().catch(()=>({message:"停止失败"})) as {message?:string};setError(body.message??"停止失败");return}setStatus("正在停止…");return}if(!jobID)return;await fetch(`${apiBase}/v1/generation-jobs/${jobID}/cancel`,{method:"POST",headers:{Authorization:`Bearer ${token}`}});setStatus("正在停止…")}
  async function retry(id=jobID){if(!id)return;setError("");setToolNotice("");const response=await fetch(`${apiBase}/v1/generation-jobs/${id}/retry`,{method:"POST",headers:{Authorization:`Bearer ${token}`}});const payload=await response.json() as {id?:string;message?:string};if(!response.ok||!payload.id){setError(payload.message??"重试失败");return};setJobID(payload.id);setStatus("正在重试…");await consumeEvents(payload.id)}
  async function retryAgentRun(id:string){if(!id||jobID||agentRunID)return;setError("");setToolNotice("");setStatus("正在重试…");const response=await fetch(`${apiBase}/v1/agent-runs/${id}/retry`,{method:"POST",headers:{"Idempotency-Key":`${id}:retry`,Authorization:`Bearer ${token}`}});const payload=await response.json() as {agent_run?:AgentRun;message?:string};if(!response.ok||!payload.agent_run){setStatus("");setError(payload.message??"重试失败");return}setAgentRunID(payload.agent_run.id);await consumeAgentRun(payload.agent_run.id)}

  const latestAssistantByReply=new Map<string,string>();
  for(const item of messages){if(item.role==="assistant"&&item.reply_to_id)latestAssistantByReply.set(item.reply_to_id,item.id)}

  return (
    <section className="chatPanel" aria-label={`与${character.name}聊天`}>
      <header>
        <div className="chatIdentity"><span className={`roleAvatar moduleNavLogo moduleNavLogo-${character.module}`} aria-hidden="true"/><div><small>正在和</small><h3>{character.name}</h3></div></div>
        <button className="textButton" type="button" onClick={onClose}>返回角色</button>
      </header>
      <div className="messageList">
        {messages.length===0&&<p className="emptyState">发一条消息，开始你们的第一段对话。</p>}
        {messages.map(item=>{
          const files=generatedFiles(item.content);
          const ledgerFiles=ledgerExportFiles(item.content);
          const attached=attachedDocuments(item.content);
          const failure=runtimeFailure(item.content);
          const canRetry=Boolean(failure&&item.reply_to_id&&latestAssistantByReply.get(item.reply_to_id)===item.id);
          return <div className={`messageBubble ${item.role}`} key={item.id}>
            <small>{item.role==="user"?"你":character.name}</small>
            {visibleChatText(item.content)&&<p>{visibleChatText(item.content)}</p>}
            {attached.length>0&&<div className="messageFiles">{attached.map(file=><span key={file}>📎 {file}</span>)}</div>}
            {files.length>0&&<div className="messageFiles">{files.map(file=><button key={file.fileID} type="button" aria-label={`下载 ${file.name}`} onClick={()=>void downloadGeneratedFile(file)}>⬇ {file.name}</button>)}</div>}
            {ledgerFiles.length>0&&<div className="messageFiles">{ledgerFiles.map(file=><button key={file.exportID} type="button" aria-label={`下载 ${file.name}`} onClick={()=>void downloadLedgerExport(file)}>⬇ {file.name}</button>)}</div>}
            {canRetry&&failure&&<div className="messageFiles"><button type="button" disabled={Boolean(jobID||agentRunID)} onClick={()=>void(failure.runtime==="agent-run"?retryAgentRun(failure.id):retry(failure.id))}>重试</button></div>}
          </div>
        })}
        {status&&<p className="typingState">{status}</p>}<div ref={bottomRef}/>
      </div>
      {error&&<p className="formMessage" role="alert">{error}</p>}
      {toolNotice&&<p className="formMessage" role="status">{toolNotice}</p>}
      <form className="composer" onSubmit={send}>
        {pendingDocuments.length>0&&<div className="pendingFiles">{pendingDocuments.map(file=><span key={file.id}>📎 {file.name}<button type="button" aria-label={`移除 ${file.name}`} onClick={()=>setPendingDocuments(current=>current.filter(item=>item.id!==file.id))}>×</button></span>)}</div>}
        <textarea name="content" maxLength={8000} placeholder={character.module==="work"?`告诉${character.name}如何处理文字或附件…`:`跟${character.name}说点什么…`} onKeyDown={(event)=>{if(event.key==="Enter"&&!event.shiftKey&&!event.nativeEvent.isComposing){event.preventDefault();event.currentTarget.form?.requestSubmit()}}}/>
        <div>
          {character.module==="work"&&<label className="filePicker">{uploading?"上传中…":"＋ 文件"}<input type="file" accept=".pdf,.txt,application/pdf,text/plain" multiple disabled={uploading||Boolean(jobID)||Boolean(agentRunID)} onChange={(event)=>{void uploadDocuments(event.currentTarget.files);event.currentTarget.value=""}}/></label>}
          {jobID&&status.includes("失败")?<button type="button" onClick={()=>void retry()}>重试</button>:jobID||agentRunID?<button type="button" onClick={stop}>停止生成</button>:null}
          <button type="submit" disabled={!conversationID||Boolean(jobID)||Boolean(agentRunID)||uploading}>发送</button>
        </div>
      </form>
    </section>
  )
}

async function ensureConversation(token:string,characterID:string){const listed=await fetch(`${apiBase}/v1/conversations`,{headers:{Authorization:`Bearer ${token}`}});if(!listed.ok)throw new Error("无法加载会话");const payload=await listed.json() as {items:{id:string;character_id:string}[]};const existing=payload.items.find(item=>item.character_id===characterID);if(existing)return existing.id;const created=await fetch(`${apiBase}/v1/conversations`,{method:"POST",headers:{"Content-Type":"application/json",Authorization:`Bearer ${token}`},body:JSON.stringify({character_id:characterID})});if(!created.ok)throw new Error("无法创建会话");return ((await created.json()) as {id:string}).id}
async function loadMessages(token:string,conversationID:string){const items:Message[]=[];let afterSequence=0;let afterBubble=0;while(true){const query=new URLSearchParams({after_sequence:String(afterSequence),after_bubble:String(afterBubble),limit:"200"});const response=await fetch(`${apiBase}/v1/conversations/${conversationID}/messages?${query}`,{headers:{Authorization:`Bearer ${token}`}});if(!response.ok)throw new Error("无法加载历史消息");const page=((await response.json()) as {items:Message[]}).items;items.push(...page);if(page.length<200)break;const last=page[page.length-1];afterSequence=last.sequence;afterBubble=last.bubble}return items}
async function loadGenerationJob(token:string,jobID:string){const response=await fetch(`${apiBase}/v1/generation-jobs/${jobID}`,{headers:{Authorization:`Bearer ${token}`}});if(!response.ok)throw new Error("无法读取任务状态");return await response.json() as {status:string}}
function mergeMessage(current:Message[],incoming:Message){if(current.some(item=>item.id===incoming.id))return current;return [...current,incoming].sort((a,b)=>a.sequence-b.sequence||a.bubble-b.bubble)}
function mergeMessages(current:Message[],incoming:Message[]){return incoming.reduce(mergeMessage,current)}
function attachedDocuments(content:string){return Array.from(content.matchAll(/<!--ai-document:[^|>]+(?:\|([^>]*))?-->/g),match=>match[1]||"附件")}
function generatedFiles(content:string):GeneratedFileLink[]{return Array.from(content.matchAll(/<!--ai-generated-file:([^|>]+)\|([^|>]+)\|([^>]*)-->/g),match=>({runID:match[1],fileID:match[2],name:match[3]||"下载文件"}))}
function ledgerExportFiles(content:string):LedgerExportLink[]{return Array.from(content.matchAll(/<!--ai-ledger-export:([^|>]+)\|([^>]*)-->/g),match=>({exportID:match[1],name:match[2]||"账单.xlsx"}))}
function runtimeFailure(content:string){const match=content.match(/<!--ai-(agent-run|generation-job):([^|>]+)\|(failed|timed_out|cancelled)(?:\|[^>]*)?-->/);return match?{runtime:match[1],id:match[2]}:null}
function delay(milliseconds:number){return new Promise(resolve=>setTimeout(resolve,milliseconds))}
