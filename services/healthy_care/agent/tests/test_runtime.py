import json
from uuid import uuid4
import httpx
import pytest
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command
from app.contracts import ProviderError
from app.main import build_graph, execution, has_positive_red_flag, guard_node

def context(content='这两天喉咙痛'):
    mid, conv, run = map(str, (uuid4(), uuid4(), uuid4()))
    return {'run_id':run,'conversation_id':conv,'message_id':mid,'content':content,'messages':[{'id':mid,'role':'user','content':content,'attachments':[]}],'knowledge':[],'now':'2026-10-19T08:00:00Z','state':{'profile':[{'data':{},'version':1}],'episodes':[],'tasks':[],'observations':[],'medications':[],'consents':[]}}

@pytest.mark.asyncio
async def test_real_langgraph_interrupt_and_recompile_resume(monkeypatch):
    monkeypatch.setenv('AGENT_PROVIDER','test');monkeypatch.setenv('ENABLE_TEST_PROVIDER','true');monkeypatch.setenv('GO_API_URL','http://go.test');monkeypatch.setenv('INTERNAL_TOKEN','x'*48)
    value=context(); events=[]
    async def emit(kind,data):events.append((kind,data))
    async with httpx.AsyncClient(transport=httpx.MockTransport(lambda r:httpx.Response(200,json=value))) as client:
        token=execution.set({'client':client,'emit':emit})
        try:
            saver=InMemorySaver();graph=build_graph(saver);cfg={'configurable':{'thread_id':value['run_id']}}
            result=await graph.ainvoke({'run_id':value['run_id'],'resolved':False},cfg)
            assert result['__interrupt__']
            assert result['result']['actions'][0]['kind']=='create_episode'
            assert result['result']['provider']=='test-only'
            assert result['result']['model_config']['test_provider'] is True
            assert len(result['result']['model_config']['prompt_sha256'])==64
            assert 'MODEL_API_KEY' not in json.dumps(result['result']['model_config'])
            assert any(k=='delta' for k,d in events)
            # A newly compiled graph can resume the saved execution without rerunning the model.
            restored=build_graph(saver);before=len(events)
            resumed=await restored.ainvoke(Command(resume={'resolved':True}),cfg)
            assert resumed['resolved'] is True
            assert not any(kind in ('delta','result') or data.get('node')=='model' for kind,data in events[before:])
        finally:execution.reset(token)

@pytest.mark.asyncio
async def test_provider_is_explicitly_disabled_by_default(monkeypatch):
    monkeypatch.setenv('AGENT_PROVIDER','unconfigured')
    from app.providers import generate
    async def emit(*args):pass
    async with httpx.AsyncClient() as client:
        with pytest.raises(ProviderError,match='provider_not_configured'):
            await generate(context(),emit,client)

@pytest.mark.asyncio
async def test_test_provider_cannot_be_enabled_accidentally(monkeypatch):
    monkeypatch.setenv('AGENT_PROVIDER','test');monkeypatch.delenv('ENABLE_TEST_PROVIDER',raising=False)
    from app.providers import generate
    async def emit(*args):pass
    async with httpx.AsyncClient() as client:
        with pytest.raises(ProviderError,match='test_provider_disabled'):await generate(context(),emit,client)

@pytest.mark.parametrize('text,expected',[('我现在呼吸困难',True),('没有胸痛',False),('没有胸痛，但现在呼吸困难',True),('没有呼吸困难，也没有胸痛',False)])
def test_red_flag_negation(text,expected):assert has_positive_red_flag(text)==expected

@pytest.mark.asyncio
async def test_urgent_routing_does_not_require_a_model(monkeypatch):
    monkeypatch.setenv('AGENT_PROVIDER','unconfigured');monkeypatch.setenv('GO_API_URL','http://go.test');monkeypatch.setenv('INTERNAL_TOKEN','x'*48)
    value=context('我现在呼吸困难');value['state']['profile'][0]['data']['age']=12
    events=[];registered=[]
    async def emit(kind,data):events.append((kind,data))
    async def register(run,conversation):registered.append((run,conversation))
    async with httpx.AsyncClient(transport=httpx.MockTransport(lambda r:httpx.Response(200,json=value))) as client:
        token=execution.set({'client':client,'emit':emit,'register_thread':register})
        try:
            result=await build_graph(InMemorySaver()).ainvoke({'run_id':value['run_id'],'resolved':False},{'configurable':{'thread_id':value['run_id']}})
            assert result['result']['urgency']=='urgent'
            assert result['result']['actions']==[]
            assert '急救' in result['result']['reply']
            assert registered==[(value['run_id'],value['conversation_id'])]
        finally:execution.reset(token)

@pytest.mark.asyncio
async def test_multimodal_provider_contract_does_not_stream_unvalidated_advice(monkeypatch):
    from app.providers import openai_compatible
    monkeypatch.setenv('MODEL_API_KEY','model-test-secret');monkeypatch.setenv('MODEL_NAME','test-vision');monkeypatch.setenv('MODEL_BASE_URL','https://model.test/v1');monkeypatch.setenv('GO_API_URL','http://go.test');monkeypatch.setenv('INTERNAL_TOKEN','x'*48)
    value=context();value['messages'][0]['attachments']=['attachment-test'];events=[]
    async def emit(kind,data):events.append((kind,data))
    def handle(req):
        if req.url.host=='go.test':
            assert req.headers['authorization']=='Bearer '+'x'*48
            return httpx.Response(200,json={'data_url':'data:image/png;base64,c3ludGhldGlj'})
        assert req.headers['authorization']=='Bearer model-test-secret'
        body=json.loads(req.content);assert body['messages'][1]['content'][1]['type']=='image_url'
        output=json.dumps({'reply':'请补充图片中的位置与持续时间。','urgency':'clarify'})
        return httpx.Response(200,text='data: '+json.dumps({'choices':[{'delta':{'content':output}}]})+'\n\ndata: [DONE]\n\n')
    async with httpx.AsyncClient(transport=httpx.MockTransport(handle)) as client:
        result=await openai_compatible(value,emit,client)
        assert '补充' in result.reply
        assert not any(kind=='delta' for kind,_ in events)

@pytest.mark.asyncio
@pytest.mark.parametrize('value,code',[
    ({'reply':'请参考这份资料','sources':[{'knowledge_id':'made-up-source'}]},'unverified_source'),
    ({'reply':'建议服用2片'},'unsupported_dosage_advice'),
    ({'reply':'你已经确诊了某疾病'},'unsupported_diagnosis'),
])
async def test_unverified_clinical_output_is_not_emitted(value,code):
    events=[]
    async def emit(kind,data):events.append((kind,data))
    token=execution.set({'context':context(),'emit':emit})
    try:
        with pytest.raises(ProviderError,match=code):await guard_node({'result':value,'provider':'contract-test'})
        assert events==[]
    finally:execution.reset(token)

@pytest.mark.asyncio
@pytest.mark.parametrize('reported',[True,False])
async def test_stream_usage_is_reported_or_unknown_without_invented_cost(monkeypatch,reported):
    from app.providers import openai_compatible
    monkeypatch.setenv('MODEL_API_KEY','synthetic-secret');monkeypatch.setenv('MODEL_NAME','synthetic-model');monkeypatch.setenv('MODEL_BASE_URL','https://model.test/v1')
    monkeypatch.setenv('MODEL_STREAM_USAGE','true' if reported else 'false')
    events=[]
    async def emit(kind,data):events.append((kind,data))
    def handle(req):
        body=json.loads(req.content)
        assert ('stream_options' in body) is reported
        output=json.dumps({'reply':'合成接口测试输出','urgency':'clarify'})
        chunks=[{'choices':[{'delta':{'content':output}}]}]
        if reported:chunks.append({'choices':[],'usage':{'prompt_tokens':123,'completion_tokens':45,'total_tokens':168,'other':'must not be copied'}})
        return httpx.Response(200,text=''.join('data: '+json.dumps(chunk)+'\n\n' for chunk in chunks)+'data: [DONE]\n\n')
    async with httpx.AsyncClient(transport=httpx.MockTransport(handle)) as client:
        await openai_compatible(context(),emit,client)
    usage=next(data for kind,data in events if kind=='telemetry')
    assert usage['reported'] is reported
    assert usage['total_tokens']==(168 if reported else None)
    assert usage['cost']['amount'] is None and usage['cost']['currency'] is None
    assert 'synthetic-secret' not in json.dumps(usage) and 'must not be copied' not in json.dumps(usage)

@pytest.mark.asyncio
async def test_failed_node_records_duration_and_reason_without_private_content(monkeypatch):
    from app.main import measured_node,model_node
    monkeypatch.setenv('AGENT_PROVIDER','unconfigured')
    events=[]
    async def emit(kind,data):events.append((kind,data))
    async with httpx.AsyncClient() as client:
        token=execution.set({'context':context('private-synthetic-description'),'client':client,'emit':emit})
        try:
            with pytest.raises(ProviderError,match='provider_not_configured'):
                await measured_node('model',model_node)({})
        finally:execution.reset(token)
    assert len(events)==1
    kind,data=events[0]
    assert kind=='telemetry' and data['duration_ms']>=0 and data['status']=='failed'
    assert data['error_code']=='provider_not_configured'
    assert 'private-synthetic-description' not in json.dumps(data)

@pytest.mark.asyncio
@pytest.mark.parametrize('second_valid',[True,False])
async def test_format_retry_is_bounded_and_counts_both_attempts(monkeypatch,second_valid):
    from app.providers import openai_compatible
    monkeypatch.setenv('MODEL_API_KEY','synthetic-secret');monkeypatch.setenv('MODEL_NAME','synthetic-model');monkeypatch.setenv('MODEL_BASE_URL','https://model.test/v1');monkeypatch.setenv('MODEL_TEMPERATURE','')
    calls=[];events=[]
    async def emit(kind,data):events.append((kind,data))
    def handle(req):
        body=json.loads(req.content);calls.append(body)
        assert 'temperature' not in body
        if len(calls)==2:
            assert 'schema' in body['messages'][-1]['content']
            assert 'private-invalid-output' not in body['messages'][-1]['content']
        value={'reply':'valid synthetic response','urgency':'clarify'} if len(calls)==2 and second_valid else {'reply':'private-invalid-output','urgency':'routine|clarify'}
        chunks=[{'choices':[{'delta':{'content':json.dumps(value)}}]},{'choices':[],'usage':{'prompt_tokens':10,'completion_tokens':5,'total_tokens':15}}]
        return httpx.Response(200,text=''.join('data: '+json.dumps(c)+'\n\n' for c in chunks)+'data: [DONE]\n\n')
    async with httpx.AsyncClient(transport=httpx.MockTransport(handle)) as client:
        if second_valid:
            assert (await openai_compatible(context(),emit,client)).reply=='valid synthetic response'
        else:
            with pytest.raises(ProviderError,match='invalid_model_response'):await openai_compatible(context(),emit,client)
    assert len(calls)==2 and not any(kind=='delta' for kind,_ in events)
    usage=next(data for kind,data in events if kind=='telemetry' and data['kind']=='model_usage')
    assert usage['generation_attempts']==2 and usage['format_retries']==1 and usage['total_tokens']==30
    assert 'private-invalid-output' not in json.dumps(events)

@pytest.mark.asyncio
async def test_stream_error_is_explicit_and_not_retried(monkeypatch):
    from app.providers import openai_compatible
    monkeypatch.setenv('MODEL_API_KEY','synthetic-secret');monkeypatch.setenv('MODEL_NAME','synthetic-model');monkeypatch.setenv('MODEL_BASE_URL','https://model.test/v1')
    calls=[];events=[]
    def handle(req):
        calls.append(req)
        return httpx.Response(200,text='data: {"error":{"code":503,"message":"private-upstream-detail"}}\n\n')
    async def emit(kind,data):events.append((kind,data))
    async with httpx.AsyncClient(transport=httpx.MockTransport(handle)) as client:
        with pytest.raises(ProviderError,match='model_stream_error'):await openai_compatible(context(),emit,client)
    assert len(calls)==1 and 'private-upstream-detail' not in json.dumps(events)

@pytest.mark.parametrize('raw,expected',[('',None),('0.2',0.2),('1',1.0)])
def test_optional_model_temperature(monkeypatch,raw,expected):
    from app.providers import model_temperature
    monkeypatch.setenv('MODEL_TEMPERATURE',raw)
    assert model_temperature()==expected

@pytest.mark.parametrize('raw',['nan','-1','3','invalid'])
def test_invalid_temperature_rejected(monkeypatch,raw):
    from app.providers import model_temperature
    monkeypatch.setenv('MODEL_TEMPERATURE',raw)
    with pytest.raises(ProviderError,match='invalid_model_configuration'):model_temperature()

@pytest.mark.parametrize('proposed_version',[None,999])
def test_action_version_bound_to_read_snapshot(proposed_version):
    from app.main import bind_action_versions
    from app.contracts import ModelResult
    value=context();value['state']['episodes']=[{'id':'episode-owned','version':7}]
    result=ModelResult(reply='确认后保存',actions=[{'kind':'checkin','payload':{'episode_id':'episode-owned','score':3},'expected_version':proposed_version}])
    bind_action_versions(result,value)
    assert result.actions[0].expected_version==7
    assert result.actions[0].payload=={'episode_id':'episode-owned','score':3}
    # A later database version is not silently substituted for this snapshot.
    assert result.actions[0].expected_version!=8

def test_unknown_action_resource_is_rejected():
    from app.main import bind_action_versions
    from app.contracts import ModelResult
    result=ModelResult(reply='确认后保存',actions=[{'kind':'checkin','payload':{'episode_id':'not-in-user-context'}}])
    with pytest.raises(ProviderError,match='invalid_action_reference'):bind_action_versions(result,context())

@pytest.mark.asyncio
async def test_urgent_help_precedes_invalid_model_sources_and_actions():
    events=[]
    async def emit(kind,data):events.append((kind,data))
    token=execution.set({'context':context(),'emit':emit})
    try:
        result=await guard_node({'provider':'contract-test','result':{'reply':'服用2片','urgency':'urgent','sources':[{'knowledge_id':'unknown-source'}],'actions':[{'kind':'checkin','payload':{'episode_id':'foreign'}}]}})
        assert result['result']['urgency']=='urgent' and '急救' in result['result']['reply']
        assert result['result']['actions']==[] and result['result']['sources']==[]
        assert '服用2片' not in json.dumps(events,ensure_ascii=False)
    finally:execution.reset(token)
