"""Editable capability-map composition shared by browser and native PowerPoint."""
from __future__ import annotations

from typing import Any


def default_regions(items: list[dict[str, Any]]) -> dict[str, Any]:
    ids = [item['id'] for item in items]
    if not 2 <= len(ids) <= 10:
        raise ValueError('分层图解需要2–10个内容模块；请合并或分成多页')
    if len(ids) <= 4:
        return dict(lead='', main=ids, aside='', foundation=[], footer='', flow='none')
    if len(ids) <= 7:
        n = max(3, len(ids)-3)
        return dict(lead='', main=ids[:n], aside='', foundation=ids[n:], footer='', flow='up')
    n = 4 if len(ids) == 10 else 3
    return dict(lead=ids[0], main=ids[1:n+1], aside=ids[n+1], foundation=ids[n+2:-1], footer=ids[-1], flow='down')


def validate_regions(value: Any, items: list[dict[str, Any]]) -> dict[str, Any]:
    if value is None:
        value = default_regions(items)
    if not isinstance(value, dict) or value.get('flow') not in {'up', 'down', 'none'}:
        raise ValueError('图解层级或关系方向无效')
    regions = {key: value.get(key, '' if key in {'lead', 'aside', 'footer'} else []) for key in ('lead', 'main', 'aside', 'foundation', 'footer')}
    if any(not isinstance(regions[k], str) for k in ('lead', 'aside', 'footer')):
        raise ValueError('图解区域必须引用内容模块')
    if not isinstance(regions['main'], list) or not 1 <= len(regions['main']) <= 4 or not isinstance(regions['foundation'], list) or len(regions['foundation']) > 3:
        raise ValueError('图解支持1–4个主要分区、最多3个支撑分区')
    ids = [regions[k] for k in ('lead', 'aside', 'footer') if regions[k]] + regions['main'] + regions['foundation']
    expected = [item['id'] for item in items]
    if any(not isinstance(key, str) for key in ids) or len(ids) != len(set(ids)) or set(ids) != set(expected):
        raise ValueError(f'图解必须完整且不重复地保留所有内容模块；需要{expected}，收到{ids}')
    return {**regions, 'flow': value['flow']}


def capability_scene(slide: dict[str, Any], deck: dict[str, Any], theme: dict[str, str]) -> list[dict[str, Any]]:
    items = {item['id']: item for item in slide['items']}
    regions = validate_regions(slide.get('regions'), slide['items'])
    nodes: list[dict[str, Any]] = []
    ink, accent, muted = theme['ink'], theme['accent'], theme['muted']
    radius = deck.get('typography', {}).get('radius', 12)

    def mix(color: str, base: str, proportion: float) -> str:
        return ''.join(f'{round(int(color[i:i+2],16)*proportion+int(base[i:i+2],16)*(1-proportion)):02X}' for i in (0,2,4))

    def text(key: str, value: str, x: float, y: float, w: float, h: float, size: float, *, bold: bool = False, color: str = ink, group: int = 0) -> None:
        if key != 'title':
            size = max(16, size * deck.get('typography', {}).get('body_size', 21) / 21)
        nodes.append(dict(id=key, type='text', text=value, x=x, y=y, w=w, h=h, font_size=size, font=theme['heading_font'] if bold else theme['body_font'], color=color, bold=bold, group=group))

    def panel(key: str, x: float, y: float, w: float, h: float, color: str, group: int) -> None:
        nodes.append(dict(id=key, type='shape', kind='roundrect', x=x, y=y, w=w, h=h, fill=color, radius=radius, group=group))

    def arrow(key: str, x: float, y: float, w: float, h: float, group: int, direction: str = 'down') -> None:
        nodes.append(dict(id=key, type='connector', x=x, y=y, w=w, h=h, color=muted, direction=direction, group=group))

    def parts(key: str) -> tuple[str, str]:
        title, _, body = items[key]['text'].partition('\n')
        return title, body

    title_x = 96 if slide.get('image_id') else 36
    if slide.get('image_id'):
        nodes.append(dict(id='image', type='image', x=32, y=24, w=52, h=52, asset_id=slide['image_id'], fit='contain', group=0))
    text('title', slide['title'], title_x, 31, 924-title_x, 56, deck.get('typography', {}).get('title_size', 32), bold=True)
    lead, main, aside, foundation, footer = (regions[k] for k in ('lead', 'main', 'aside', 'foundation', 'footer'))
    if lead:
        panel('lead-panel', 36, 105, 888, 68, theme['surface'], 0)
        heading, body = parts(lead)
        text(lead+'-heading', heading, 54, 119, 208, 44, 22, bold=True, color=accent)
        text(lead, body, 280, 119, 626, 44, 18)
    top = 210 if lead else 118
    bottom = 354 if foundation else 448 if footer else 474
    height = bottom-top
    width = 672 if aside else 888
    gap = 16
    cell = (width-gap*(len(main)-1))/len(main)
    tones = ['FBE4EC', 'FFF0CC', 'E5EBFC', 'E7F3EE'] if deck['theme_id'] == 'companion' and not deck['brand'] else [mix(accent, theme['background'], v) for v in (.08, .12, .16, .20)]
    for i, key in enumerate(main):
        x, group = 36+i*(cell+gap), i+1
        if lead and regions['flow'] != 'none':
            arrow(f'scenario-link-{i}', x+cell/2, 174, 0, 27, group, regions['flow'])
        panel(f'scenario-panel-{i}', x, top, cell, height, tones[i], group)
        heading, body = parts(key)
        text(key+'-heading', heading, x+18, top+14, cell-36, 42, 21, bold=True, group=group)
        text(key, body, x+18, top+62, cell-36, height-74, 18, group=group)
    if aside:
        panel('aside-panel', 738, top, 186, height, theme['surface'], 5)
        heading, body = parts(aside)
        text(aside+'-heading', heading, 754, top+14, 154, 40, 20, bold=True, group=5)
        text(aside, body, 754, top+54, 154, height-58, 16, group=5)
        if regions['flow'] != 'none':
            arrow('aside-link', 710, top+height/2, 20, 0, 5)
    for i, key in enumerate(foundation):
        w = (888-24*(len(foundation)-1))/len(foundation)
        x = 36+i*(w+24)
        if regions['flow'] != 'none':
            arrow(f'foundation-link-{i}', x+w/2, 361, 0, 16, 6, 'up')
        panel(f'foundation-panel-{i}', x, 384, w, 68 if footer else 90, theme['surface'], 6)
        heading, body = parts(key)
        text(key+'-heading', heading, x+18, 393, w-36, 27, 19, bold=True, color=accent, group=6)
        text(key, body, x+18, 423, w-36, 25 if footer else 44, 16, group=6)
    if footer:
        heading, body = parts(footer)
        text(footer+'-heading', heading, 40, 467, 184, 25, 16, bold=True, group=7)
        text(footer, body, 242, 467, 678, 25, 16, color=muted, group=7)
    return nodes
