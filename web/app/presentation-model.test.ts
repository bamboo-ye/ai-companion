import assert from "node:assert/strict";
import test from "node:test";
import { acceptDesign, commit, editItems, insertSlide, instantiateTemplate, moveSlide, parseChart, parseDeck, parseTable, parseTheme, redo, removeSlide, renameDeck, replaceSlideImage, splitSlide, undo, updateSlide } from "./presentation-model.ts";
import type { Deck, DesignSlide } from "./presentation-model.ts";

const slide: DesignSlide = { id: "s1", title: "流程", layout: "steps", items: [{ id: "a", text: "第一项", source: "资料1" }, { id: "b", text: "第二项" }, { id: "c", text: "第三项" }], notes: "讲稿", source: "", locked: false, image_id: "", image_fit: "contain", animation: "inherit", duration_ms: 450, advance: "click", overrides: {} };
const deck: Deck = { version: "presentation-design-v2", id: "deck", title: "测试", audience: "团队", theme_id: "business", mode: "present", animation: "subtle", revision: 1, brand: {}, assets: {}, slides: [slide, { ...slide, id: "s2" }] };
test("inserting a preset preserves neighboring pages, brand and source data and can be undone", () => {
  const preset = { ...slide, chart: { labels: ["甲"], values: [3], series: "示例", unit: "" }, locked: true };
  const inserted = insertSlide(deck, "s1", preset, "new");
  assert.deepEqual(inserted.slides.map(s => s.id), ["s1", "new", "s2"]);
  assert.equal(inserted.theme_id, deck.theme_id);
  assert.equal(inserted.slides[1].locked, false);
  assert.deepEqual(inserted.slides[1].chart, preset.chart);
  inserted.slides[1].items[0].text = "new content";
  assert.equal(preset.items[0].text, "第一项");
  const history = commit({ present: deck, past: [], future: [] }, inserted);
  assert.deepEqual(undo(history).present, deck);
  assert.deepEqual(redo(undo(history)).present.slides, inserted.slides);
});
test("inserting rejects missing selection, duplicate IDs, missing pictures and the page limit", () => {
  assert.throws(() => insertSlide(deck, "missing", slide, "new"));
  assert.throws(() => insertSlide(deck, "s1", slide, "s2"));
  assert.throws(() => insertSlide(deck, "s1", { ...slide, image_id: "missing" }, "new"));
  assert.throws(() => insertSlide({ ...deck, slides: Array.from({ length: 60 }, (_, i) => ({ ...slide, id: `s${i}` })) }, "s1", slide, "new"));
});
test("template instances have independent identity and do not mutate library contents", () => {
  const one = instantiateTemplate(deck, "one"), two = instantiateTemplate(deck, "two");
  assert.notEqual(one.id, two.id);
  assert.notEqual(one.slides[0].id, two.slides[0].id);
  one.slides[0].items[0].text = "edited";
  assert.equal(two.slides[0].items[0].text, slide.items[0].text);
});
test("renaming can sync an editable cover but respects locked covers and keeps data intact", () => {
  const covered = { ...deck, slides: [{ ...slide, layout: "cover" }, deck.slides[1]] };
  const renamed = renameDeck(covered, " 新的标题 ", "新听众", true);
  assert.equal(renamed.title, "新的标题"); assert.equal(renamed.slides[0].title, "新的标题");
  assert.deepEqual(renamed.slides[0].items, slide.items);
  assert.equal(renameDeck(covered, "新标题", "", false).slides[0].title, slide.title);
  assert.equal(renameDeck({ ...covered, slides: [{ ...covered.slides[0], locked: true }] }, "新标题", "", true).slides[0].title, slide.title);
  assert.throws(() => renameDeck(covered, " ", "", true));
});
test("deleting images releases only unreferenced assets while undo restores the whole page", () => {
  const asset = { data_base64: "picture", media_type: "image/png", source: "source.png" };
  const pictured = { ...deck, slides: [{ ...slide, image_id: "picture" }, deck.slides[1]], assets: { picture: asset } };
  const removed = removeSlide(pictured, "s1");
  assert.deepEqual(removed.assets, {});
  assert.equal(undo(commit({ present: pictured, past: [], future: [] }, removed)).present.assets.picture, asset);
  assert.equal(removeSlide({ ...pictured, slides: pictured.slides.map(s => ({ ...s, image_id: "picture" })) }, "s1").assets.picture, asset);
  assert.equal(removeSlide({ ...pictured, slides: [{ ...slide, locked: true }, deck.slides[1]] }, "s1").slides.length, 2);
  assert.equal(removeSlide({ ...deck, slides: [slide] }, "s1").slides.length, 1);
});
test("replacing a copied page's picture never changes the original and releases unused assets", () => {
  const asset = { data_base64: "old", media_type: "image/png", source: "old.png" };
  const pictured = { ...deck, assets: { old: asset }, slides: deck.slides.map(s => ({ ...s, image_id: "old" })) };
  const replaced = replaceSlideImage(pictured, "s2", "new", { ...asset, data_base64: "new" });
  assert.equal(replaced.slides[0].image_id, "old");
  assert.equal(replaced.assets.old.data_base64, "old");
  assert.equal(replaced.assets.new.data_base64, "new");
  assert.deepEqual(Object.keys(replaceSlideImage(replaced, "s2", "").assets), ["old"]);
  assert.equal(pictured.slides[1].image_id, "old");
  const locked = { ...pictured, slides: pictured.slides.map(s => ({ ...s, locked: true })) };
  assert.equal(replaceSlideImage(locked, "s2", "new", asset), locked);
});
test("edits undo and redo preserve stable IDs, sources and notes", () => {
  const edited = updateSlide(deck, "s1", { title: "新的流程" });
  const history = commit({ past: [], present: deck, future: [] }, edited);
  assert.equal(history.present.slides[0].title, "新的流程");
  assert.equal(history.present.slides[0].items[0].source, "资料1");
  assert.deepEqual(undo(history).present, deck);
  assert.deepEqual(redo(undo(history)).present, history.present);
  assert.equal(deck.slides[0].title, "流程");
});
test("locked slides reject content edits but can be unlocked", () => {
  const locked = updateSlide(deck, "s1", { locked: true });
  assert.equal(updateSlide(locked, "s1", { title: "不应改变" }).slides[0].title, "流程");
  assert.equal(updateSlide(locked, "s1", { locked: false }).slides[0].locked, false);
});
test("splitting retains every source item exactly once and preserves notes", () => {
  const split = splitSlide(deck, "s1", "continued");
  assert.deepEqual(split.slides.slice(0, 2).flatMap(s => s.items), slide.items);
  assert.equal(split.slides[1].notes, slide.notes);
  assert.equal(split.slides[1].id, "continued");
});
test("reordering handles boundaries and preserves identity", () => {
  assert.deepEqual(moveSlide(deck, "s1", -1), deck);
  assert.deepEqual(moveSlide(deck, "s1", 1).slides.map(s => s.id), ["s2", "s1"]);
});
test("chart parsing supports negative values but rejects invented or nonfinite data", () => {
  assert.deepEqual(parseChart("一月,12.5\n二月，-3", "收入", "万").values, [12.5, -3]);
  assert.throws(() => parseChart("一月,NaN", "", ""));
  assert.throws(() => parseChart("一月,", "", ""));
  assert.throws(() => parseChart("", "", ""));
});
test("editing text retains block identity and provenance", () => {
  const items = editItems(slide.items, "修改第一项\n第二项");
  assert.equal(items[0].id, "a"); assert.equal(items[0].source, "资料1");
});
test("design import rejects unrelated JSON and incompatible versions", () => {
  assert.equal(parseDeck(JSON.stringify(deck)).id, deck.id);
  assert.throws(() => parseDeck('{"slides":[]}'));
  assert.throws(() => parseDeck('{"version":"other","slides":[{}]}'));
});
test("accepting repaired server geometry does not leave an unsaved revision", () => {
  const repaired = { ...deck, slides: [{ ...slide, layout: "bullets" }] };
  const history = acceptDesign({ past: [], present: deck, future: [] }, repaired);
  assert.deepEqual(history.present, repaired);
  assert.deepEqual(undo(history).present, deck);
});
test("splitting a pictured slide retains the image exactly once", () => {
  const source = { ...deck, slides: [{ ...slide, image_id: "image" }] };
  const result = splitSlide(source, "s1", "s3");
  assert.equal(result.slides.filter(s => s.image_id === "image").length, 1);
  assert.equal(result.slides[0].layout, "image-right");
});
test("new text block IDs cannot collide with imported IDs", () => {
  const result = editItems([{ id: "item-1", text: "first" }], "first\nsecond");
  assert.equal(new Set(result.map(i => i.id)).size, 2);
});
test("tables require rectangular records and themes allow only supported properties", () => {
  assert.deepEqual(parseTable("阶段 | 状态\n调研 | 完成").rows, [["调研", "完成"]]);
  assert.throws(() => parseTable("阶段 | 状态\n调研"));
  assert.throws(() => parseTable("阶段 | 状态"));
  assert.deepEqual(parseTheme('{"version":"presentation-theme-v1","theme_id":"business","brand":{"accent":"ABCDEF"}}').brand, { accent: "ABCDEF" });
  assert.throws(() => parseTheme('{"version":"presentation-theme-v1","theme_id":"business","brand":{"accent":"url(x)"}}'));
});
