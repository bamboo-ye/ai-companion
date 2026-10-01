"""Reviewed, public product facts for project-name presentation requests.

Keep this release snapshot aligned with README.zh-CN.md and the built-in product
knowledge catalog. It is not a source of user records, private config or metrics.
"""
from __future__ import annotations

import base64
from pathlib import Path
from typing import Any

PROJECT_ALIASES = {"伴ai", "伴 ai", "ai companion", "ai-companion", "ai_companion"}


def is_companion(value: Any) -> bool:
    return isinstance(value, str) and value.strip().casefold() in PROJECT_ALIASES


def project_deck(payload: dict[str, Any]) -> dict[str, Any]:
    from ai_companion_worker.presentation_design import VERSION, plan_deck, validate_deck

    if not is_companion(payload.get("project_name")):
        raise ValueError("未找到内置项目，请提供项目资料后生成演示")
    content = [
        ("identity", "1 个伴AI\n陪你聊，帮你记，和你一起把事情做好"),
        ("companion", "情感陪伴\n自定义角色\n持续多轮对话\n延续偏好与话题"),
        ("life", "生活助手\n今日计划与提醒\n自然语言记账\n收支汇总与导出"),
        ("work", "工作伙伴\n多份资料研读\n内容整理与审校\n办公成果生成"),
        ("tools", "可扩展技能\nPPT 制作\nPDF 翻译\n文档编辑\n表格分析"),
        ("memory", "长期记忆\n跨会话保留偏好，可查看与纠正"),
        ("knowledge", "知识 Wiki\n资料编译与检索，沿引用回到原文"),
        ("platform", "统一运行与管理\nAgent 编排　受控工具　模型路由　权限与额度"),
    ]
    assets: dict[str, Any] = {}
    logo = Path('/app/.assets/presentation-logo.png')
    if not logo.is_file():
        logo = Path(__file__).resolve().parents[4] / 'web/public/icons/logo.png'
    if logo.is_file():
        assets['companion-logo'] = {'media_type': 'image/png', 'data_base64': base64.b64encode(logo.read_bytes()).decode(), 'source': '伴AI 原始品牌图标：web/public/icons/logo.png'}
    single = payload.get('slide_count', 1) == 1
    title = str(payload.get('title') or ('伴AI 项目能力全景' if single else '伴AI 项目介绍')).strip()
    source = '伴AI内置产品知识 · 产品能力概览'
    notes = '内容依据：README.zh-CN.md「一个工作台，三种陪伴」「系统架构」；internal/productknowledge/content/catalog.json；web/app/styles.css 品牌配色。模型服务需按部署配置启用，内置项目知识不含用户资料或运行指标。'
    if not single:
        # Feed reviewed facts through the same multi-page planner as any project.
        facts = dict(content)
        brief = '## 产品定位\n- ' + facts['identity'].split('\n', 1)[1]
        brief += '\n## 能力全景\n' + '\n'.join('### '+value.split('\n', 1)[0]+'\n- '+value.split('\n', 1)[1].replace('\n', '\n- ') for _, value in content)
        for key in ('companion', 'life', 'work'):
            heading, body = facts[key].split('\n', 1)
            brief += '\n## '+heading+'\n- '+body.replace('\n', '\n- ')
            if key == 'work':
                brief += '\n- '+facts['tools'].replace('\n', '，', 1).replace('\n', '、')
        brief += '\n## 记忆、知识与运行支持\n'+'\n'.join('- '+facts[key].replace('\n', '：', 1) for key in ('memory', 'knowledge', 'platform'))
        deck = plan_deck({**payload, 'title': title, 'brief': brief, 'theme_id': 'companion'})
        for page in deck['slides']:
            page.update(source=source, notes=notes)
            for item in page['items']:
                item['source'] = source
        deck['assets'] = assets
        overview = next(page for page in deck['slides'] if page['layout'] == 'capability-map')
        overview['image_id'] = 'companion-logo' if assets else ''
        return validate_deck(deck)
    return validate_deck({
        'version': VERSION, 'id': 'builtin-ai-companion', 'title': title,
        'audience': payload.get('audience', '项目介绍听众'), 'theme_id': 'companion',
        'mode': payload.get('presentation_mode', 'present'),
        'animation': payload.get('animation', 'explain'), 'assets': assets,
        'slides': [{'id': 'companion-overview', 'title': title, 'layout': 'capability-map',
                    'image_id': 'companion-logo' if assets else '',
                    'items': [{'id': key, 'text': value, 'source': 'README.zh-CN.md / 一个工作台，三种陪伴；内置产品知识'} for key, value in content],
                    'source': source, 'notes': notes}],
    })
