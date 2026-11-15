import asyncio
import json
import logging
import math
import os
import re
from datetime import datetime, timedelta
from typing import Callable, Awaitable
import httpx
from .contracts import ModelResult, ProviderError
from .prompt import SYSTEM_PROMPT
from pydantic import ValidationError

log = logging.getLogger('healthycare.provider')

Emit = Callable[[str, dict], Awaitable[None]]

def model_temperature() -> float | None:
    raw = os.getenv('MODEL_TEMPERATURE', '0.2').strip()
    if not raw:
        return None
    try:
        value = float(raw)
        if not math.isfinite(value) or not 0 <= value <= 2:
            raise ValueError
        return value
    except ValueError:
        raise ProviderError('invalid_model_configuration')

async def openai_compatible(context: dict, emit: Emit, client: httpx.AsyncClient,tool_headers:dict|None=None) -> ModelResult:
    key, model = os.getenv('MODEL_API_KEY', ''), os.getenv('MODEL_NAME', '')
    if not key or not model:
        raise ProviderError('provider_not_configured')
    content: list[dict] = [{'type': 'text', 'text': json.dumps(context, ensure_ascii=False)}]
    latest = next((m for m in reversed(context['messages']) if m['id'] == context['message_id']), {})
    for attachment in latest.get('attachments', []):
        response = await client.get(f"{os.environ['GO_API_URL']}/internal/runs/{context['run_id']}/attachments/{attachment}", headers={'Authorization': f"Bearer {os.environ['INTERNAL_TOKEN']}",**(tool_headers or {})})
        response.raise_for_status()
        content.append({'type': 'image_url', 'image_url': {'url': response.json()['data_url']}})
    body = {'model': model, 'messages': [{'role': 'system', 'content': SYSTEM_PROMPT}, {'role': 'user', 'content': content}], 'response_format': {'type': 'json_object'}, 'stream': True}
    temperature = model_temperature()
    if temperature is not None:
        body['temperature'] = temperature
    if os.getenv('MODEL_STREAM_USAGE', 'true') == 'true':
        body['stream_options'] = {'include_usage': True}
    attempts = []
    async def usage_event():
        fields = ('prompt_tokens', 'completion_tokens', 'total_tokens')
        totals = {field: sum(a[field] for a in attempts) if all(a[field] is not None for a in attempts) else None for field in fields}
        await emit('telemetry', {'kind': 'model_usage', 'reported': any(a['reported'] for a in attempts), **totals, 'generation_attempts': len(attempts), 'format_retries': len(attempts)-1, 'attempts': attempts, 'cost': {'amount': None, 'currency': None, 'status': 'price_not_configured'}})
    # One format-only retry is safe: model output has not reached users or tools.
    # Authentication, HTTP, safety validation and business errors are not retried here.
    for attempt in range(2):
        output = []; usage = {'reported': False, 'prompt_tokens': None, 'completion_tokens': None, 'total_tokens': None}
        async with client.stream('POST', os.getenv('MODEL_BASE_URL', 'https://api.openai.com/v1').rstrip('/') + '/chat/completions', headers={'Authorization': f'Bearer {key}'}, json=body) as response:
            if response.status_code in (401, 403):
                raise ProviderError('model_authentication_failed')
            if response.status_code == 429:
                raise ProviderError('model_rate_limited')
            response.raise_for_status()
            async for line in response.aiter_lines():
                if not line.startswith('data:'):
                    continue
                raw = line[5:].strip()
                if raw == '[DONE]':
                    break
                try:
                    chunk = json.loads(raw)
                    if not isinstance(chunk, dict): raise ValueError('invalid chunk')
                    if chunk.get('error'):
                        raise ProviderError('model_stream_error')
                    reported = chunk.get('usage')
                    if isinstance(reported, dict):
                        for field in ('prompt_tokens', 'completion_tokens', 'total_tokens'):
                            value = reported.get(field)
                            if isinstance(value, int) and not isinstance(value, bool) and 0 <= value <= 10**12:
                                usage[field] = value; usage['reported'] = True
                    choices = chunk.get('choices') or []
                    delta = choices[0].get('delta', {}).get('content', '') if choices else ''
                except (ValueError, IndexError, TypeError, AttributeError):
                    raise ProviderError('invalid_model_response')
                if isinstance(delta, str):
                    output.append(delta)
                    if sum(map(len, output)) > 100000:
                        raise ProviderError('model_output_too_large')
                    # Raw fragments never become user-visible advice.
                    await emit('node', {'node': 'model', 'status': 'generating', 'attempt': attempt+1, 'characters': sum(map(len, output))})
        attempts.append(usage)
        try:
            result = ModelResult.model_validate_json(''.join(output))
        except ValidationError as exc:
            # Field names/values may be supplied by users, so log types only.
            codes = sorted({e['type'] for e in exc.errors(include_input=False, include_url=False)})
            log.warning('Model JSON validation failed: attempt=%s types=%s', attempt+1, codes)
            if attempt:
                await usage_event()
                raise ProviderError('invalid_model_response')
            await emit('telemetry', {'kind': 'model_format_retry', 'attempt': 1, 'validation_types': codes})
            body['messages'] = [*body['messages'], {'role': 'system', 'content': '上一响应没有通过JSON结构校验。请重新输出符合以下schema的JSON对象，不添加字段、不使用Markdown、不改变用户事实。urgency必须是单个枚举值。Schema: '+json.dumps(ModelResult.model_json_schema(),ensure_ascii=False)}]
            continue
        await usage_event()
        return result
    raise ProviderError('invalid_model_response')

async def test_provider(context: dict, emit: Emit) -> ModelResult:
    """Explicit test-only deterministic provider; never a production fallback."""
    if os.getenv('ENABLE_TEST_PROVIDER') != 'true':
        raise ProviderError('test_provider_disabled')
    if '延迟验收' in context['content']:
        await asyncio.sleep(3)
    text = context['content']; state = context['state']; actions = []
    active = next((e for e in state['episodes'] if e['status'] == 'active'), None)
    now = datetime.fromisoformat(context['now'].replace('Z', '+00:00'))
    if '合成背景完整' in text:
        actions=[{'kind':'update_profile','payload':{'age':28,'region':'ZZ','pregnancy_status':'not_applicable','allergies':[],'conditions':[],'current_medications':[]},'expected_version':state['profile'][0]['version']}]
    elif '合成说明记录' in text and context.get('catalog'):
        p=next(p for p in context['catalog'] if p['id']=='synthetic-product')
        actions=[{'kind':'record_medication','payload':{'name':p['name'],'product_id':p['id'],'leaflet_version':p['version'],'region':p['region'],'strength':p['strength'],'form':p['form'],'route':p['route'],'ingredients':p['ingredients'],'amount':'2ml','used_at':now.isoformat(),'state':'used'}}]
    elif '清除' in text or '删除所有' in text:
        actions = [{'kind': 'delete_records', 'payload': {'scope': 'health'}}]
    elif '归档' in text or '结束跟进' in text or '已经好了' in text:
        if active:
            actions = [{'kind': 'archive_episode', 'payload': {'episode_id': active['id'], 'reason': text}, 'expected_version': active['version']}]
    elif '提醒' in text:
        if '暂停' in text and state['tasks']:
            task = state['tasks'][0]
            actions = [{'kind': 'update_task', 'payload': {'task_id': task['id'], 'status': 'paused'}, 'expected_version': task['version']}]
        else:
            due = now + timedelta(minutes=1)
            actions = [{'kind': 'create_task', 'payload': {'title': '记录今天的情况', 'due_at': due.isoformat(), 'recurrence': 'daily' if '每天' in text else 'once'}}]
    elif '我在' in text or '岁' in text:
        payload = {}
        age = re.search(r'(\d{1,3})岁', text)
        if age: payload['age'] = int(age[1])
        city = re.search(r'我在([^，。\s]+)', text)
        if city: payload['city'] = city[1]
        actions = [{'kind': 'update_profile', 'payload': payload, 'expected_version': state['profile'][0]['version']}]
    elif '更正用药' in text and state['medications']:
        med=state['medications'][0]
        actions=[{'kind':'update_medication','payload':{'medication_id':med['id'],'state':'not_used' if '未用' in text else 'unknown','note':text},'expected_version':med['version']}]
    elif '用药' in text and '产品' in text:
        product = re.search(r'产品([A-Za-z0-9]+)', text)
        actions = [{'kind': 'record_medication', 'payload': {'name': '测试产品' + (product[1] if product else 'A'), 'state': 'used', 'ingredients': ['测试成分甲'] if '成分甲' in text else [], 'note': text, **({'episode_id': active['id']} if active else {})}, 'expected_version': active['version'] if active else None}]
    elif active and any(v in text for v in ['今天', '分', '没用药', '记不清']):
        score = re.search(r'(\d+(?:\.\d+)?)分', text)
        payload = {'episode_id': active['id'], 'note': text}
        if score: payload['score'] = float(score[1])
        if '没用药' in text: payload['medication_state'] = 'not_used'
        elif '记不清' in text: payload['medication_state'] = 'unknown'
        actions = [{'kind': 'checkin', 'payload': payload, 'expected_version': active['version']}]
    elif not active and any(v in text for v in ['不舒服', '喉咙痛', '皮肤痒', '头痛', '肚子痛', '鼻痒']):
        topic = '皮肤不适' if '皮肤' in text else '胃肠不适' if '肚子' in text else '感冒鼻咽'
        actions = [{'kind': 'create_episode', 'payload': {'complaint': text, 'topic': topic, **({'started_at':now.isoformat()} if '开始时间未知' not in text else {})}}]
    reply = '这是自动验收专用的测试输出，不是医学建议。' + ('已整理待确认操作，请核对后确认。' if actions else '今天有什么不舒服，或有什么健康问题想了解？')
    await emit('node', {'node': 'test_provider', 'status': 'completed'})
    return ModelResult(reply=reply, actions=actions, extracted={'self_report': text}, urgency='clarify')

async def generate(context: dict, emit: Emit, client: httpx.AsyncClient,tool_headers:dict|None=None) -> tuple[ModelResult, str]:
    provider = os.getenv('AGENT_PROVIDER', 'unconfigured')
    try:
        if provider == 'test': return await test_provider(context, emit), 'test-only'
        if provider == 'openai_compatible': return await openai_compatible(context, emit, client,tool_headers), provider
        raise ProviderError('provider_not_configured')
    except httpx.TimeoutException:
        raise ProviderError('model_timeout')
    except httpx.HTTPError:
        raise ProviderError('model_upstream_error')
