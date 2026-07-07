import fs from "node:fs/promises";
import { SpreadsheetFile, Workbook } from "@oai/artifact-tool";

const outputPath = process.argv[2];
if (!outputPath) throw new Error("output path is required");
let stdin = "";
for await (const chunk of process.stdin) stdin += chunk;
const payload = JSON.parse(stdin);

const wallTime = (value, timeZone) => {
  const parts = Object.fromEntries(new Intl.DateTimeFormat("en-CA", {
    timeZone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  }).formatToParts(new Date(value)).filter((part) => part.type !== "literal").map((part) => [part.type, Number(part.value)]));
  return new Date(Date.UTC(parts.year, parts.month - 1, parts.day, parts.hour, parts.minute, parts.second));
};
const localLabel = (value, timeZone) => new Intl.DateTimeFormat("zh-CN", {
  timeZone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
}).format(new Date(value)).replaceAll("/", "-");

const workbook = Workbook.create();
const detail = workbook.worksheets.add("账单明细");
const summary = workbook.worksheets.add("月度汇总");
detail.showGridLines = false;
summary.showGridLines = false;

detail.getRange("A1:H1").merge();
detail.getRange("A1").values = [[`伴AI 账单明细 · ${payload.month}`]];
detail.getRange("A1:H1").format = { fill: "#C65D2E", font: { bold: true, color: "#FFFFFF", size: 16 }, rowHeight: 32 };
detail.getRange("A2:H2").merge();
detail.getRange("A2").values = [[`生成时间：${localLabel(payload.generated_at, payload.timezone)} · 报表时区：${payload.timezone}`]];
detail.getRange("A2:H2").format = { fill: "#FFF3E8", font: { color: "#6B4A36", size: 10 }, rowHeight: 22 };
detail.getRange("A5:H5").values = [["发生时间", "收支方向", "分类", "商户", "备注", "币种", "金额", "记录 ID"]];
detail.getRange("A5:H5").format = { fill: "#6B7C5A", font: { bold: true, color: "#FFFFFF" }, rowHeight: 24 };
const rows = payload.entries.map((entry) => [wallTime(entry.occurred_at, payload.timezone), entry.direction, entry.category, entry.merchant, entry.note, entry.currency, entry.amount_minor / 100, entry.id]);
if (rows.length > 0) {
  detail.getRangeByIndexes(5, 0, rows.length, 8).values = rows;
  detail.getRange(`A6:A${5 + rows.length}`).format.numberFormat = "yyyy-mm-dd hh:mm";
  detail.getRange(`G6:G${5 + rows.length}`).format.numberFormat = "#,##0.00";
  detail.getRange(`A6:H${5 + rows.length}`).format.borders = { preset: "inside", style: "thin", color: "#E7DDD4" };
  detail.tables.add(`A5:H${5 + rows.length}`, true, "LedgerEntriesTable").style = "TableStyleMedium4";
}
detail.freezePanes.freezeRows(5);
detail.getRange("A:H").format.autofitColumns();
detail.getRange("A:A").format.columnWidth = 20;
detail.getRange("D:E").format.columnWidth = 18;
detail.getRange("H:H").format.columnWidth = 38;

summary.getRange("A1:F1").merge();
summary.getRange("A1").values = [[`伴AI 月度账本 · ${payload.month}`]];
summary.getRange("A1:F1").format = { fill: "#C65D2E", font: { bold: true, color: "#FFFFFF", size: 16 }, rowHeight: 32 };
summary.getRange("A2:B2").values = [["统计币种", payload.currency]];
summary.getRange("A4:A7").values = [["收入"], ["支出"], ["结余"], ["记录数"]];
summary.getRange("A4:A7").format = { fill: "#FFF3E8", font: { bold: true, color: "#6B4A36" } };
const dataEnd = Math.max(105, 5 + rows.length);
summary.getRange("B4").formulas = [[`=SUMIFS('账单明细'!$G$6:$G$${dataEnd},'账单明细'!$B$6:$B$${dataEnd},"income",'账单明细'!$F$6:$F$${dataEnd},$B$2)`]];
summary.getRange("B5").formulas = [[`=SUMIFS('账单明细'!$G$6:$G$${dataEnd},'账单明细'!$B$6:$B$${dataEnd},"expense",'账单明细'!$F$6:$F$${dataEnd},$B$2)`]];
summary.getRange("B6").formulas = [["=B4-B5"]];
summary.getRange("B7").formulas = [[`=COUNTIFS('账单明细'!$F$6:$F$${dataEnd},$B$2)`]];
summary.getRange("B4:B6").format.numberFormat = "#,##0.00";
summary.getRange("A4:B7").format.borders = { preset: "outside", style: "thin", color: "#D7C5B8" };
const categories = [...new Set(payload.entries.filter((entry) => entry.direction === "expense" && entry.currency === payload.currency).map((entry) => entry.category))].sort();
summary.getRange("A10:B10").values = [["支出分类", "金额"]];
summary.getRange("A10:B10").format = { fill: "#6B7C5A", font: { bold: true, color: "#FFFFFF" } };
categories.forEach((category, index) => {
  const row = 11 + index;
  summary.getRange(`A${row}`).values = [[category]];
  summary.getRange(`B${row}`).formulas = [[`=SUMIFS('账单明细'!$G$6:$G$${dataEnd},'账单明细'!$B$6:$B$${dataEnd},"expense",'账单明细'!$C$6:$C$${dataEnd},A${row},'账单明细'!$F$6:$F$${dataEnd},$B$2)`]];
});
if (categories.length > 0) {
  summary.getRange(`B11:B${10 + categories.length}`).format.numberFormat = "#,##0.00";
  const chart = summary.charts.add("bar", summary.getRange(`A10:B${10 + categories.length}`));
  chart.title = "支出分类分布";
  chart.hasLegend = false;
  chart.yAxis = { numberFormatCode: "#,##0.00" };
  chart.setPosition("D3", "K18");
}
summary.getRange("A:B").format.autofitColumns();
summary.getRange("A:A").format.columnWidth = 18;
summary.getRange("B:B").format.columnWidth = 16;
const xlsx = await SpreadsheetFile.exportXlsx(workbook);
await xlsx.save(outputPath);
