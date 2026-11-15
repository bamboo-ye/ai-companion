import asyncio
from contextlib import asynccontextmanager
from contextvars import ContextVar
import hmac
import hashlib
import json
import logging
import inspect
import time
import os
import re
from typing import TypedDict, Any
from uuid import UUID
from urllib.parse import urlsplit
import httpx
from fastapi import FastAPI, Request, HTTPException
from fastapi.responses import StreamingResponse
from langgraph.graph import StateGraph, START, END
from langgraph.types import Command, interrupt
from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver
from psycopg_pool import AsyncConnectionPool
from .contracts import RunRequest, ProviderError, ModelResult
from .providers import generate, model_temperature

log = logging.getLogger('healthycare.agent')
execution: ContextVar[dict] = ContextVar('execution')

class State(TypedDict, total=False):
    run_id: str
    conversation_id: str
    result: dict[str, Any]
    provider: str
    resolved: bool
    preemptive_urgent: bool
    model_config: dict[str,Any]

async def load_context(state: State):
    env = execution.get()
    response = await env['client'].get(f"{os.environ['GO_API_URL']}/internal/runs/{state['run_id']}/context", headers={'Authorization': f"Bearer {os.environ['INTERNAL_TOKEN']}",**env.get('tool_headers',{})})
    response.raise_for_status()
    env['context'] = response.json()
    if env.get('register_thread'):
        await env['register_thread'](state['run_id'], env['context']['conversation_id'])
    await env['emit']('node', {'node': 'context', 'status': 'completed'})
    return {'conversation_id': env['context']['conversation_id']}

async def model_node(state: State):
    env = execution.get()
    async def record_emit(kind, data):
        if kind == 'telemetry' and data.get('kind') == 'model_usage': env['model_usage'] = data
        await env['emit'](kind, data)
    result, provider = await generate(env['context'], record_emit, env['client'],env.get('tool_headers'))
    from .prompt import SYSTEM_PROMPT
    configuration={'provider':provider,'model_name':os.getenv('MODEL_NAME') if provider=='openai_compatible' else None,'endpoint_host':urlsplit(os.getenv('MODEL_BASE_URL','')).hostname if provider=='openai_compatible' else None,'temperature':model_temperature() if provider=='openai_compatible' else None,'response_format':'json_object','prompt_sha256':hashlib.sha256(SYSTEM_PROMPT.encode()).hexdigest(),'context_sha256':hashlib.sha256(json.dumps(env['context'],ensure_ascii=False,sort_keys=True,separators=(',',':')).encode()).hexdigest(),'test_provider':provider=='test-only'}
    return {'result': result.model_dump(), 'provider': provider,'model_config':configuration}

async def triage_node(state: State):
    if has_positive_red_flag(execution.get()['context']['content']):
        result=ModelResult(reply='你描述了需要优先求助的异常表现，请立即寻求当地急救或急诊帮助，不要等待聊天分析。',urgency='urgent')
        return {'preemptive_urgent':True,'result':result.model_dump(),'provider':'deterministic-safety-routing'}
    return {'preemptive_urgent':False}

def has_positive_red_flag(content: str) -> bool:
    for clause in re.split(r'[，。！？；\n]|但是|不过|但', content):
        for term in ('呼吸困难', '喘不上气', '意识不清', '突然剧烈头痛', '胸痛'):
            pos = clause.find(term)
            if pos >= 0 and not re.search(r'(没有|无|否认|未出现|不是).{0,6}$', clause[:pos]):
                return True
    return False

def bind_action_versions(result: ModelResult, context: dict):
    """Use the read snapshot's technical versions, never model-invented versions.

    Go still checks ownership and the CURRENT version at confirmation time.
    This binds metadata, not health facts or permission decisions.
    """
    state = context['state']
    for action in result.actions:
        resource = None
        if action.kind == 'update_profile':
            resource = next(iter(state.get('profile', [])), None)
        else:
            field = collection = None
            if action.kind in ('checkin', 'archive_episode') or (action.kind in ('create_task', 'record_medication') and action.payload.get('episode_id')):
                field, collection = 'episode_id', 'episodes'
            elif action.kind == 'update_medication':
                field, collection = 'medication_id', 'medications'
            elif action.kind == 'update_task':
                field, collection = 'task_id', 'tasks'
            if field:
                resource = next((r for r in state.get(collection, []) if r.get('id') == action.payload.get(field)), None)
                if resource is None:
                    raise ProviderError('invalid_action_reference')
        if resource is not None:
            version = resource.get('version')
            if type(version) is not int or version < 1:
                raise ProviderError('invalid_action_reference')
            action.expected_version = version
        elif action.kind == 'update_profile':
            raise ProviderError('invalid_action_reference')
        else:
            action.expected_version = None

async def guard_node(state: State):
    env = execution.get(); context = env['context']; result = ModelResult.model_validate(state['result'])
    if has_positive_red_flag(context['content']) or result.urgency == 'urgent':
        result.reply = '你描述了需要优先求助的异常表现，建议立即寻求当地急救或急诊帮助，不要等待聊天分析。可保留当前描述，方便向医护人员说明。'
        result.actions = []; result.sources = []; result.urgency = 'urgent'
    knowledge = {k['id']: k for k in context['knowledge']}
    sources = []
    for source in result.sources:
        if source.knowledge_id not in knowledge: raise ProviderError('unverified_source')
        k = knowledge[source.knowledge_id]
        sources.append({key: k[key] for key in ('id', 'title', 'source_url', 'version', 'review_status')})
    if len({p.kind for p in result.actions}) != len(result.actions): raise ProviderError('duplicate_action_kind')
    bind_action_versions(result, context)
    # No reviewed product-level instructions are shipped in the reference bundle.
    if re.search(r'(每次|每天|每日|一次|服用|口服|外用|可以.{0,4}[吃服用]).{0,8}\d+(?:\.\d+)?\s*(片|毫克|mg|毫升|ml|粒)', result.reply, re.I):
        raise ProviderError('unsupported_dosage_advice')
    if re.search(r'你(?:已经)?(?:确诊|就是患有|肯定患有)', result.reply): raise ProviderError('unsupported_diagnosis')
    ages = [p['data'].get('age') for p in context['state']['profile'] if isinstance(p.get('data'), dict)]
    if result.urgency != 'urgent' and ages and isinstance(ages[0], (int, float)) and ages[0] < 18:
        result.reply = '我可以帮你记录变化，但首版个体自主用药信息面向适用的成年人。请与监护人及医护人员确认相关情况。'
    value = result.model_dump(); value['sources'] = sources; value['provider'] = state['provider']
    value['model_config']=state.get('model_config',{'provider':state['provider'],'model_used':False})
    await env['emit']('node', {'node': 'validation', 'status': 'completed'})
    # Only validated prose is emitted; raw JSON/model fragments never become advice.
    for start in range(0, len(result.reply), 48):
        await env['emit']('delta', {'text': result.reply[start:start + 48]})
    return {'result': value}

def confirmation_node(state: State):
    if state['result'].get('actions') and not state.get('resolved'):
        resolved = interrupt({'kind': 'confirm_actions', 'run_id': state['run_id']})
        if not isinstance(resolved, dict) or resolved.get('resolved') is not True:
            raise ProviderError('invalid_resume')
    return {'resolved': True}

def measured_node(name, handler):
    async def measured(state):
        env = execution.get(None); started = time.perf_counter(); status = 'completed'; code = None
        try:
            return await handler(state) if inspect.iscoroutinefunction(handler) else handler(state)
        except BaseException as exc:
            status = 'waiting_user' if type(exc).__name__ == 'GraphInterrupt' else 'cancelled' if isinstance(exc, asyncio.CancelledError) else 'failed'
            code = getattr(exc, 'code', None) if status == 'failed' else None
            raise
        finally:
            if env is not None:
                value = {'kind': 'node_duration', 'node': name, 'duration_ms': round((time.perf_counter()-started)*1000, 3), 'status': status}
                if code: value['error_code'] = code
                env.setdefault('node_metrics', []).append(value)
                await env['emit']('telemetry', value)
    return measured

def attempt_metrics(env, started, status, checkpoint_reused=False):
    return {'kind': 'attempt', 'duration_ms': round((time.perf_counter()-started)*1000, 3), 'status': status, 'checkpoint_reused': checkpoint_reused, 'nodes': env.get('node_metrics', []), 'model_usage': env.get('model_usage', {'reported': False, 'prompt_tokens': None, 'completion_tokens': None, 'total_tokens': None, 'cost': {'amount': None, 'currency': None, 'status': 'not_reported'}})}

def build_graph(checkpointer):
    builder = StateGraph(State)
    builder.add_node('context', measured_node('context', load_context)); builder.add_node('triage', measured_node('triage', triage_node)); builder.add_node('model', measured_node('model', model_node))
    builder.add_node('validate', measured_node('validate', guard_node)); builder.add_node('confirmation', measured_node('confirmation', confirmation_node))
    builder.add_edge(START, 'context'); builder.add_edge('context', 'triage')
    builder.add_conditional_edges('triage',lambda state:'validate' if state.get('preemptive_urgent') else 'model',{'validate':'validate','model':'model'})
    builder.add_edge('model', 'validate')
    builder.add_edge('validate', 'confirmation'); builder.add_edge('confirmation', END)
    return builder.compile(checkpointer=checkpointer)

@asynccontextmanager
async def lifespan(app: FastAPI):
    token = os.getenv('INTERNAL_TOKEN', '')
    if len(token) < 32: raise RuntimeError('INTERNAL_TOKEN must contain at least 32 characters')
    if os.getenv('AGENT_PROVIDER') == 'test' and os.getenv('ENABLE_TEST_PROVIDER') != 'true':
        raise RuntimeError('Test provider is not allowed without ENABLE_TEST_PROVIDER=true')
    async with AsyncConnectionPool(os.environ['AGENT_DATABASE_URL'],min_size=1,max_size=6,kwargs={'autocommit':True,'prepare_threshold':0},check=AsyncConnectionPool.check_connection,open=False) as pool:
        await pool.open();await pool.wait()
        saver=AsyncPostgresSaver(pool)
        await saver.setup()
        async with pool.connection() as conn:
            await conn.execute('CREATE TABLE IF NOT EXISTS run_threads (run_id text PRIMARY KEY, conversation_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now())')
            await conn.execute('CREATE TABLE IF NOT EXISTS deleted_conversations (conversation_id text PRIMARY KEY, deleted_at timestamptz NOT NULL DEFAULT now())')
        app.state.pool=pool
        app.state.running={}
        app.state.saver = saver; app.state.graph = build_graph(saver)
        async with httpx.AsyncClient(timeout=httpx.Timeout(float(os.getenv('MODEL_TIMEOUT_SECONDS', '90'))), follow_redirects=False, trust_env=False) as client:
            app.state.client = client
            yield

app = FastAPI(title='伴AI健康伙伴 Agent Runtime', lifespan=lifespan, docs_url=None, redoc_url=None)

@app.middleware('http')
async def internal_auth(request: Request, call_next):
    if request.url.path.startswith('/internal/'):
        expected = 'Bearer ' + os.getenv('INTERNAL_TOKEN', '')
        if not hmac.compare_digest(request.headers.get('Authorization', ''), expected):
            from fastapi.responses import JSONResponse
            return JSONResponse({'error': 'unauthorized'}, status_code=401)
    return await call_next(request)

@app.get('/healthz')
async def health():
    provider = os.getenv('AGENT_PROVIDER', 'unconfigured')
    return {'agent_available': True, 'provider': provider, 'test_provider': provider == 'test', 'model_configured': provider == 'openai_compatible' and bool(os.getenv('MODEL_API_KEY') and os.getenv('MODEL_NAME'))}

@app.post('/internal/runs')
async def run(body: RunRequest):
    try: UUID(body.run_id)
    except ValueError: raise HTTPException(400, 'invalid_run_id')
    queue: asyncio.Queue = asyncio.Queue()
    async def emit(kind, value): await queue.put({'type': kind, 'data': value})
    async def register_thread(run_id, conversation_id):
        if run_id in app.state.running:app.state.running[run_id]['conversation_id']=conversation_id
        async with app.state.pool.connection() as conn:
            cursor=await conn.execute('INSERT INTO run_threads(run_id,conversation_id) SELECT %s,%s WHERE NOT EXISTS(SELECT 1 FROM deleted_conversations WHERE conversation_id=%s) ON CONFLICT(run_id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id', (run_id, conversation_id,conversation_id))
            if cursor.rowcount!=1:raise ProviderError('conversation_deleted')
    async def drive():
        env = {'client': app.state.client, 'emit': emit, 'register_thread': register_thread, 'tool_headers': {'X-Run-Access':body.tool_access}, 'node_metrics': []}
        binding = execution.set(env); started = time.perf_counter(); checkpoint_reused = False
        try:
            config = {'configurable': {'thread_id': body.run_id}, 'recursion_limit': 12}
            saved = await app.state.graph.aget_state(config)
            if saved.values.get('result') and (not saved.next or saved.next == ('confirmation',)):
                result = saved.values['result']; checkpoint_reused = True
            else:
                if saved.values:
                    await load_context({'run_id': body.run_id})
                result_state = await app.state.graph.ainvoke(None if saved.values else {'run_id': body.run_id, 'resolved': False}, config)
                result = result_state['result']
            metrics = attempt_metrics(env, started, 'waiting_user' if result.get('actions') else 'succeeded', checkpoint_reused)
            await emit('telemetry', metrics)
            await emit('result', {**result, 'runtime_metrics': metrics})
        except ProviderError as exc:
            await emit('telemetry', attempt_metrics(env, started, 'failed', checkpoint_reused))
            await emit('error', {'code': exc.code})
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            log.error('Agent execution failed: %s', type(exc).__name__)
            await emit('telemetry', attempt_metrics(env, started, 'failed', checkpoint_reused))
            await emit('error', {'code': 'agent_failed'})
        finally:
            execution.reset(binding)
            await queue.put(None)
    async def stream():
        task = asyncio.create_task(drive())
        app.state.running[body.run_id]={'task':task,'conversation_id':None}
        try:
            while True:
                item = await queue.get()
                if item is None: break
                yield json.dumps(item, ensure_ascii=False) + '\n'
        finally:
            if not task.done(): task.cancel()
            await asyncio.gather(task, return_exceptions=True)
            app.state.running.pop(body.run_id,None)
    return StreamingResponse(stream(), media_type='application/x-ndjson')

@app.post('/internal/runs/{run_id}/resume')
async def resume(run_id: str, body: dict):
    try: UUID(run_id)
    except ValueError: raise HTTPException(400, 'invalid_run_id')
    config = {'configurable': {'thread_id': run_id}}
    saved = await app.state.graph.aget_state(config)
    if saved.next:
        await app.state.graph.ainvoke(Command(resume={'resolved': body.get('resolved') is True}), config)
    return {'ok': True}

@app.delete('/internal/conversations/{conversation_id}')
async def delete_conversation(conversation_id: str):
    try: UUID(conversation_id)
    except ValueError: raise HTTPException(400, 'invalid_conversation_id')
    # A tombstone also prevents a context fetched just before deletion from
    # registering its thread after the cleanup scan has finished.
    async with app.state.pool.connection() as conn:
        await conn.execute('INSERT INTO deleted_conversations(conversation_id) VALUES(%s) ON CONFLICT DO NOTHING',(conversation_id,))
    tasks=[run['task'] for run in list(app.state.running.values()) if run['conversation_id']==conversation_id]
    for task in tasks:task.cancel()
    if tasks:await asyncio.gather(*tasks,return_exceptions=True)
    async with app.state.pool.connection() as conn:
        cursor = await conn.execute('SELECT run_id FROM run_threads WHERE conversation_id=%s', (conversation_id,))
        threads=await cursor.fetchall()
    for row in threads:await app.state.saver.adelete_thread(row[0])
    async with app.state.pool.connection() as conn:
        await conn.execute('DELETE FROM run_threads WHERE conversation_id=%s', (conversation_id,))
    return {'ok': True}
