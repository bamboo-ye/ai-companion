from typing import Any, Literal
from pydantic import BaseModel, Field, ConfigDict

KINDS = Literal['create_episode', 'checkin', 'record_medication', 'update_medication', 'archive_episode', 'update_profile', 'create_task', 'update_task', 'delete_records']

class Action(BaseModel):
    model_config = ConfigDict(extra='forbid')
    kind: KINDS
    payload: dict[str, Any]
    expected_version: int | None = None

class Source(BaseModel):
    model_config = ConfigDict(extra='forbid')
    knowledge_id: str

class ModelResult(BaseModel):
    model_config = ConfigDict(extra='forbid')
    reply: str = Field(min_length=1, max_length=12000)
    actions: list[Action] = Field(default_factory=list, max_length=6)
    sources: list[Source] = Field(default_factory=list, max_length=8)
    extracted: dict[str, Any] = Field(default_factory=dict)
    urgency: Literal['routine', 'clarify', 'seek_care', 'urgent'] = 'clarify'

class RunRequest(BaseModel):
    run_id: str
    tool_access: str

class ProviderError(Exception):
    def __init__(self, code: str):
        self.code = code
        super().__init__(code)
