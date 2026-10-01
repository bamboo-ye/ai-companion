"use client";

import type { Deck, Scene, SceneNode } from "./presentation-model";

function ChartPreview({ node }: { node: SceneNode }) {
  const chart = node.chart!;
  const { x, y, w, h } = node;
  const min = Math.min(0, ...chart.values), max = Math.max(0, ...chart.values), range = max - min || 1;
  const bar = node.chart_type === "bar", left = bar ? 148 : 76, plotW = w - left - 60, plotH = h - 64;
  const zeroX = x + left + -min / range * plotW, zeroY = y + 12 + max / range * plotH;
  const points = chart.values.map((v, i) => `${x + left + (i + .5) * plotW / chart.values.length},${y + 12 + (max - v) / range * plotH}`).join(" ");
  return <g fontFamily={`${node.font}, sans-serif`} fontSize={12} fill="currentColor">
    <line x1={bar ? zeroX : x + 76} x2={bar ? zeroX : x + w - 20} y1={bar ? y + 12 : zeroY} y2={bar ? y + h - 52 : zeroY} stroke="currentColor" opacity=".3"/>
    {node.chart_type === "line" && <polyline points={points} fill="none" stroke={`#${node.color}`} strokeWidth="3"/>}
    {chart.values.map((value, i) => {
      const unitW = plotW / chart.values.length, unitH = plotH / chart.values.length;
      const cx = x + left + (i + .5) * unitW, cy = y + 12 + (max - value) / range * plotH;
      return <g key={i}>
        {bar ? <><text x={x + 65} y={y + 12 + (i + .55) * unitH} textAnchor="end">{chart.labels[i]}</text><rect x={value >= 0 ? zeroX : zeroX + value / range * plotW} y={y + 12 + i * unitH + 5} width={Math.abs(value) / range * plotW} height={Math.max(4, unitH - 14)} fill={`#${node.color}`}/><text x={zeroX + value / range * plotW + (value >= 0 ? 5 : -5)} y={y + 12 + (i + .55) * unitH} textAnchor={value >= 0 ? "start" : "end"}>{value}{chart.unit}</text></> : <>
          {node.chart_type === "line" ? <circle cx={cx} cy={cy} r={4} fill={`#${node.color}`}/> : <rect x={cx - unitW * .3} y={Math.min(zeroY, cy)} width={unitW * .6} height={Math.max(1, Math.abs(zeroY - cy))} fill={`#${node.color}`}/>}
          <text x={cx} y={cy - 8} textAnchor="middle">{value}{chart.unit}</text><text x={cx} y={y + h - 24} textAnchor="middle">{chart.labels[i]}</text>
        </>}
      </g>;
    })}
  </g>;
}

function TablePreview({ node }: { node: SceneNode }) {
  let y = node.y;
  const width = node.w / node.table!.columns.length;
  return <g fontFamily={node.font} fontSize={node.font_size}>
    {node.cell_lines?.map((row, ri) => {
      const rowY = y, height = node.row_heights![ri]; y += height;
      return <g key={ri}>{row.map((lines, ci) => <g key={ci}>
        <rect x={node.x + ci * width} y={rowY} width={width} height={height} fill={`#${ri ? node.fill : node.accent}`} stroke="#ffffff66"/>
        <text x={node.x + ci * width + 9} y={rowY + 7 + node.font_size!} fill={`#${ri ? node.color : node.header_color}`} fontWeight={ri ? 400 : 700}>{lines.map((line, li) => <tspan key={li} x={node.x + ci * width + 9} dy={li ? node.font_size! * 1.3 : 0}>{line || " "}</tspan>)}</text>
      </g>)}</g>;
    })}
  </g>;
}

export function PresentationCanvas({ scene, deck, selectedNode, onSelect, revealed = Infinity, animate = false }: { scene: Scene; deck: Deck; selectedNode?: string; onSelect?: (id: string) => void; revealed?: number; animate?: boolean }) {
  return <svg className="presentationCanvas" viewBox="0 0 960 540" role="img" aria-label={`幻灯片预览：${deck.slides.find(s => s.id === scene.id)?.title ?? ""}`} style={{ background: `#${scene.background}`, color: `#${scene.foreground || (deck.theme_id === "technology" ? "F3F7FF" : "172B4D")}` }}>
    {scene.nodes.map(node => {
      const visible = node.group <= revealed;
      const asset = node.asset_id ? deck.assets[node.asset_id] : undefined;
      return <g key={node.id} onClick={onSelect ? () => onSelect(node.id) : undefined} opacity={visible ? 1 : 0} className={animate && visible && node.group > 0 && node.group === revealed ? "presentationAppear" : ""} style={{ cursor: onSelect ? "pointer" : undefined, animationDuration: `${scene.duration_ms}ms` }}>
        {node.type === "shape" && (node.kind === "ellipse" ? <ellipse cx={node.x + node.w / 2} cy={node.y + node.h / 2} rx={node.w / 2} ry={node.h / 2} fill={`#${node.fill}`}/> : <rect x={node.x} y={node.y} width={node.w} height={node.h} rx={node.kind === "roundrect" ? node.radius ?? 12 : 0} fill={`#${node.fill}`}/>)}
        {node.type === "connector" && <g stroke={`#${node.color}`} strokeWidth={1.3} fill="none"><line x1={node.x} y1={node.y} x2={node.x+node.w} y2={node.y+node.h}/><path d={node.h === 0 ? `M${node.x+node.w-5},${node.y-3} L${node.x+node.w},${node.y} L${node.x+node.w-5},${node.y+3}` : node.direction === "up" ? `M${node.x-3},${node.y+5} L${node.x},${node.y} L${node.x+3},${node.y+5}` : `M${node.x-3},${node.y+node.h-5} L${node.x},${node.y+node.h} L${node.x+3},${node.y+node.h-5}`}/></g>}
        {node.type === "text" && <text x={node.x} y={node.y + (node.font_size ?? 18)} fontSize={node.font_size} fill={`#${node.color}`} fontFamily={`"${node.font}", "PingFang SC", "Microsoft YaHei", sans-serif`} fontWeight={node.bold ? 700 : 400} xmlSpace="preserve">{node.lines?.map((line, i) => <tspan key={i} x={node.x} dy={i ? node.line_height : 0}>{line || " "}</tspan>)}</text>}
        {node.type === "image" && asset && <image href={`data:${asset.media_type};base64,${asset.data_base64}`} x={node.x} y={node.y} width={node.w} height={node.h} preserveAspectRatio={node.fit === "cover" ? "xMidYMid slice" : "xMidYMid meet"}/>}
        {node.type === "chart" && node.chart && <ChartPreview node={node}/>}
        {node.type === "table" && node.table && <TablePreview node={node}/>}
        {selectedNode === node.id && <rect x={node.x - 2} y={node.y - 2} width={node.w + 4} height={node.h + 4} fill="none" stroke="#E78342" strokeWidth="2" strokeDasharray="5 3"/>}
      </g>;
    })}
  </svg>;
}
