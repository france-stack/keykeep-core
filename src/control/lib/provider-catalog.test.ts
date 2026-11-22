import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { PROVIDER_CATALOG, catalogGroups, findCatalogEntry, type CatalogEntry } from "./provider-catalog";
import { ICON_ON_DARK_TILE, ICON_ON_LIGHT_TILE, iconTileKind } from "./provider-icon-contrast";
import { PROVIDER_ICON_IDS } from "./provider-icons";

const PROTOCOLS = new Set(["anthropic", "openai", "gemini", "vertex", "xai", "codex"]);

/** 条目可用的端点集合：baseUrl 加上 endpoints / regions 里声明的每一个。 */
function endpointsOf(entry: CatalogEntry): string[] {
  const out = [entry.baseUrl, ...Object.values(entry.endpoints ?? {})];
  for (const region of entry.regions ?? []) {
    out.push(region.baseUrl, ...Object.values(region.endpoints ?? {}));
  }
  return out.filter((url) => url.length > 0);
}

describe("provider catalog", () => {
  it("id 唯一且协议合法", () => {
    const ids = PROVIDER_CATALOG.map((entry) => entry.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const entry of PROVIDER_CATALOG) {
      expect(PROTOCOLS.has(entry.protocol)).toBe(true);
      expect(entry.name.length).toBeGreaterThan(0);
    }
  });

  it("ready 条目必须有 https 端点；needs-config 必须留空并写明原因", () => {
    for (const entry of PROVIDER_CATALOG) {
      if (entry.status === "ready") {
        const endpoints = endpointsOf(entry);
        expect(endpoints.length).toBeGreaterThan(0);
        for (const url of endpoints) expect(url.startsWith("https://")).toBe(true);
      }
      if (entry.status === "needs-config") {
        expect(entry.baseUrl).toBe("");
        expect(entry.note?.length ?? 0).toBeGreaterThan(0);
      }
    }
  });

  it("暂不支持条目置灰且写明原因", () => {
    for (const entry of PROVIDER_CATALOG.filter((item) => item.status === "unsupported")) {
      expect(entry.note?.length ?? 0).toBeGreaterThan(0);
      expect(entry.baseUrl).toBe("");
    }
  });

  it("不包含订阅登录类供应商", () => {
    const forbidden = ["chatgpt", "copilot", "workbuddy", "qoder", "zcode", "opencode-zen", "clinepass", "claude-code"];
    for (const entry of PROVIDER_CATALOG) {
      expect(forbidden).not.toContain(entry.id);
    }
  });

  // 用户明确要求：Coding Plan / Token Plan / Pay as you go 这类「同一家的另一个计费套餐」
  // 不进目录 —— 买了套餐的人会自己填 Key 和地址，目录只留官方默认端点与真实的地理站点差异。
  it("不包含计费套餐变体", () => {
    const planWords = /coding plan|token plan|tokenhub|pay as you go|pay-as-you-go|step plan|enterprise plan/i;
    const planIds = [
      "kimi-code",
      "zhipu-coding",
      "zai-coding",
      "stepfun-plan",
      "xiaomi-mimo-plan",
      "baidu-qianfan-tokenplan",
      "tencent-tokenhub",
    ];
    for (const entry of PROVIDER_CATALOG) {
      expect(planIds).not.toContain(entry.id);
      expect(entry.name).not.toMatch(planWords);
      for (const region of entry.regions ?? []) {
        expect(region.name).not.toMatch(planWords);
        expect(region.baseUrl).not.toMatch(/token-plan|tokenhub|\/plan\/|\/coding\//);
      }
    }
  });

  it("分组非空且可按 id 查找", () => {
    const groups = catalogGroups();
    expect(groups.map((item) => item.group)).toEqual(["native", "compatible", "aggregator", "unsupported"]);
    for (const group of groups) expect(group.entries.length).toBeGreaterThan(0);
    expect(findCatalogEntry("deepseek")?.protocol).toBe("openai");
    expect(findCatalogEntry("gemini")?.protocol).toBe("gemini");
    expect(findCatalogEntry("missing-id")).toBeUndefined();
  });

  it("OpenDesign 走 Anthropic 兼容端点", () => {
    const entry = findCatalogEntry("opendesign");
    expect(entry?.group).toBe("aggregator");
    expect(entry?.protocol).toBe("anthropic");
    expect(entry?.baseUrl).toBe("https://amr-link.open-design.ai");
    expect(entry?.modelsHint).toContain("deepseek-v4.1-flash");
    expect(entry?.auth.header).toBe("Authorization");
  });

  it("多端点条目：endpoints 的键必须是合法协议，值必须是 https", () => {
    for (const entry of PROVIDER_CATALOG) {
      for (const [protocol, url] of Object.entries(entry.endpoints ?? {})) {
        expect(PROTOCOLS.has(protocol)).toBe(true);
        expect(url.startsWith("https://")).toBe(true);
      }
    }
  });

  it("区域条目：每项至少一个端点，id 在同一家内唯一", () => {
    for (const entry of PROVIDER_CATALOG.filter((item) => item.regions?.length)) {
      const ids = entry.regions!.map((region) => region.id);
      expect(new Set(ids).size).toBe(ids.length);
      for (const region of entry.regions!) {
        expect(PROTOCOLS.has(region.protocol)).toBe(true);
        expect(region.name.length).toBeGreaterThan(0);
        const endpoints = endpointsOf({ ...entry, baseUrl: region.baseUrl, endpoints: region.endpoints });
        expect(endpoints.length).toBeGreaterThan(0);
        for (const url of endpoints) expect(url.startsWith("https://")).toBe(true);
      }
      // 默认区域必须与条目自身的 baseUrl 一致，避免「打开表单看到 A、保存成 B」。
      expect(entry.regions![0].baseUrl).toBe(entry.baseUrl);
    }
  });

  it("authHeader 只在 Anthropic 形状的条目上允许 both", () => {
    for (const entry of PROVIDER_CATALOG) {
      if (entry.authHeader === "both") expect(entry.protocol).toBe("anthropic");
    }
  });

  it("catalogIds 指向 models.dev，且不与条目 id 混淆", () => {
    for (const entry of PROVIDER_CATALOG) {
      for (const id of entry.catalogIds ?? []) {
        expect(id.length).toBeGreaterThan(0);
        expect(PROTOCOLS.has(id)).toBe(false);
      }
    }
  });

  // 删掉 7 个计费套餐变体后是 54 条。这里锁死数量，防止以后误删条目或把套餐变体重新加回来。
  it("目录规模固定为 54 条", () => {
    expect(PROVIDER_CATALOG.length).toBe(54);
  });

  // 用户要求名称用厂商真实名字（中文品牌名或官方英文名），不允许拼音与生造词。
  it("知名厂商使用真实品牌名", () => {
    const expected: Record<string, string> = {
      kimi: "Kimi",
      "kimi-intl": "Kimi（国际站）",
      zhipu: "智谱 GLM",
      zai: "智谱 GLM（国际站）",
      minimax: "MiniMax",
      "minimax-intl": "MiniMax（国际站）",
      stepfun: "阶跃星辰",
      "xiaomi-mimo": "小米 MiMo",
      "baidu-qianfan": "百度千帆",
      "tencent-hunyuan": "腾讯混元",
      bailian: "阿里云百炼",
      "iflytek-spark": "讯飞星火",
      "volcengine-ark": "火山方舟",
      "huawei-cloud": "华为云 MaaS",
      longcat: "美团 LongCat",
      baichuan: "百川智能",
      qwen: "通义千问（DashScope）",
      siliconflow: "硅基流动",
      modelscope: "魔搭 ModelScope",
      aihubmix: "AiHubMix",
      yylx: "鱼鱼连线 (Yuyu Link)",
    };
    for (const [id, name] of Object.entries(expected)) {
      expect(findCatalogEntry(id)?.name).toBe(name);
    }
  });
});

describe("provider icons", () => {
  const dir = fileURLToPath(new URL("../public/provider-icons/", import.meta.url));
  const files = readdirSync(dir).filter((name) => name.endsWith(".svg"));
  const onDisk = new Set(files.map((name) => name.replace(/\.svg$/, "")));
  const catalogIds = new Set(PROVIDER_CATALOG.map((entry) => entry.id));

  /** 每个图标的文件内容（id → 文本）。断言分多条，共用这一份读取。 */
  const contents = new Map(
    files.map((file) => [file.replace(/\.svg$/, ""), readFileSync(`${dir}/${file}`, "utf8")]),
  );

  it("downloaded icons are valid SVGs and match catalog ids", () => {
    expect(files.length).toBeGreaterThanOrEqual(15);
    for (const file of files) {
      const id = file.replace(/\.svg$/, "");
      expect(catalogIds.has(id)).toBe(true);
      const content = contents.get(id)!.trimStart();
      expect(content.startsWith("<svg") || content.startsWith("<?xml")).toBe(true);
      // 全目录不能有「200 但其实是整页 HTML」的产物：那会让 <img> 静默失败。
      expect(content.slice(0, 2048).toLowerCase()).not.toContain("<html");
    }
  });

  it("PROVIDER_ICON_IDS 与磁盘上的图标一一对应", () => {
    // 组件用这份清单决定要不要发 <img> 请求；多一项就是 404，少一项就是白丢图标。
    expect([...PROVIDER_ICON_IDS].sort()).toEqual([...onDisk].sort());
  });

  it("图标是自包含的矢量文件，能被 <img> 直接渲染", () => {
    for (const [id, raw] of contents) {
      const content = raw.trimStart();
      expect(content, id).toMatch(/^<svg[\s>]/);

      // 根节点必须自带 viewBox（缩放靠它，width/height 会被 <img> 覆盖反而坏事）
      const openTag = content.slice(0, content.indexOf(">") + 1);
      expect(openTag, id).toContain("viewBox=");
      expect(openTag, id).toContain('xmlns="http://www.w3.org/2000/svg"');
      expect(openTag, id).not.toMatch(/\swidth\s*=/);
      expect(openTag, id).not.toMatch(/\sheight\s*=/);

      // 可执行内容与内嵌位图一律不允许：图标目录是静态资源，不该携带脚本
      expect(content, id).not.toMatch(/<script\b/i);
      expect(content, id).not.toMatch(/<foreignObject\b/i);
      expect(content, id).not.toMatch(/<image\b/i);
      expect(content, id).not.toMatch(/@import/i);

      // 外链：<img> 场景下不会被解析，留着只会渲染成空白
      expect(content, id).not.toMatch(/\s(?:xlink:)?href\s*=\s*["'](?:https?:)?\/\//i);
      expect(content, id).not.toMatch(/url\(\s*['"]?(?:https?:)?\/\//i);

      expect(content, id).toMatch(/<\/svg>\s*$/);
      expect(Buffer.byteLength(content, "utf8"), id).toBeLessThan(96 * 1024);
    }
  });

  it("NOTICE.md 逐条列出每个图标的来源与商标声明", () => {
    const notice = readFileSync(`${dir}/NOTICE.md`, "utf8");
    // 来源可追溯是这次改造的核心要求：每个图标都得能在 NOTICE 里找到用哪家官网/哪个库
    expect(notice).toContain("商标");
    for (const id of onDisk) {
      expect(notice, id).toContain(`| \`${id}\` |`);
    }

    // 只检查「逐条来源」那张表：类型列只能是非官网来源标记里的取值，别的来源不许悄悄混进来。
    const section = notice.split(/^## /m).find((part) => part.startsWith("逐条来源"));
    expect(section, "NOTICE.md 缺少「逐条来源」章节").toBeTruthy();
    const rows = (section ?? "").split("\n").filter((line) => line.startsWith("| `"));
    expect(rows.length).toBe(onDisk.size);
    for (const row of rows) {
      // 列：id | 来源 URL | 类型 | 底片（底片列见下面的对比度断言）
      expect(row).toMatch(/\| (official|simpleicons|iconify) \| (?:浅色底片|深色底片|—) \|$/);
      // URL 列必须是 http(s) 或取自某个官网页面，不能是本地路径
      expect(row).toMatch(/^\| `[^`]+` \| https?:\/\//);
    }
  });

  it("底片清单与图标、目录一一对应，且两种底片互斥", () => {
    // 清单是脚本按实测结论生成的，前端拿它决定要不要垫底色；错一项就是要么看不见、要么白垫。
    expect(ICON_ON_LIGHT_TILE.size).toBeGreaterThan(0);
    expect(ICON_ON_DARK_TILE.size).toBeGreaterThan(0);

    for (const id of [...ICON_ON_LIGHT_TILE, ...ICON_ON_DARK_TILE]) {
      // 只有真的有图标文件才谈得上垫底片
      expect(onDisk.has(id), `${id} 没有对应 svg`).toBe(true);
      expect(catalogIds.has(id), `${id} 不在 PROVIDER_CATALOG`).toBe(true);
    }

    const both = [...ICON_ON_LIGHT_TILE].filter((id) => ICON_ON_DARK_TILE.has(id));
    expect(both, `同时要求两种底片的 id：${both.join(", ")}`).toEqual([]);

    // iconTileKind 只是这两张表的前端视图，不能各自跑偏
    for (const id of onDisk) {
      const expected = ICON_ON_LIGHT_TILE.has(id) ? "light" : ICON_ON_DARK_TILE.has(id) ? "dark" : null;
      expect(iconTileKind(id), id).toBe(expected);
    }
    expect(iconTileKind("__not-a-provider__")).toBeNull();
  });

  it("纯黑图标必须垫浅色底片，不许被漏判成「不用垫」", () => {
    // 回归护栏：底片判定早期只看 fill 属性，把颜色写在 <style> 里的图标（如 clicline、opendesign）
    // 全部漏判，深色主题下渲染成一片空白。这里独立复算每个 svg 的墨色（规则与
    // scripts/fetch-provider-icons.mjs 的 collectInkColors 一致），防止判定又退化成读不到颜色。
    for (const [id, raw] of contents) {
      const colors = inkColors(raw);
      expect(colors.length, `${id} 解析不出任何墨色`).toBeGreaterThan(0);
      if (Math.max(...colors) < 0.25) {
        // 整张图都是深色：浅色主题看得见、深色卡片上会糊掉，必须垫浅色底片
        expect(ICON_ON_LIGHT_TILE.has(id), `${id} 全为深色，必须垫浅色底片`).toBe(true);
      }
      if (Math.min(...colors) > 0.75) {
        // 整张图都是浅色：浅色主题下白底白图，必须垫深色底片
        expect(ICON_ON_DARK_TILE.has(id), `${id} 全为浅色，必须垫深色底片`).toBe(true);
      }
    }
  });
});

/**
 * 从 svg 文本里收集所有「会被画出来」的颜色亮度（0=黑，1=白）。
 *
 * 三个来源都要看，只看 `fill=` 属性会漏掉把颜色写在 `<style>` 里的图标（clicline、opendesign 就是）：
 * 1. `<style>` 块里的 fill / stroke / stop-color 声明；
 * 2. 任意元素上的同名属性（含渐变的 `<stop stop-color>`，否则把彩色 logo 误判成纯黑）；
 * 3. 一处颜色声明都没有时，按 svg 默认的黑色算。
 */
function inkColors(svg: string): number[] {
  const colors: number[] = [];
  const push = (value: string) => {
    const brightness = brightnessOf(value);
    if (brightness !== null) colors.push(brightness);
  };

  for (const [, css] of svg.matchAll(/<style\b[^>]*>([\s\S]*?)<\/style>/gi)) {
    for (const decl of css.matchAll(/(?:fill|stroke|stop-color)\s*:\s*([^;}]+)/gi)) push(decl[1]);
  }

  let declared = 0;
  for (const attr of svg.matchAll(/\s(?:fill|stroke|stop-color)\s*=\s*(["'])(.*?)\1/gi)) {
    if (brightnessOf(attr[2]) !== null) declared += 1;
    push(attr[2]);
  }

  if (declared === 0) colors.push(0);
  return colors;
}

/** 把 svg 颜色写法折算成亮度；读不懂的（渐变引用、var()）返回 null，currentColor 按黑算。 */
function brightnessOf(raw: string): number | null {
  const value = raw.trim().toLowerCase();
  if (value === "" || value === "none" || value === "transparent") return null;
  if (value === "#fff" || value === "white" || value === "#ffffff") return 1;
  if (value === "#000" || value === "black" || value === "#000000") return 0;
  if (value === "currentcolor") return 0; // <img> 里 currentColor 解析为黑色

  const hex = value.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i);
  if (hex) {
    const digits = hex[1].length === 3 ? [...hex[1]].map((ch) => ch + ch).join("") : hex[1];
    const [r, g, b] = [0, 2, 4].map((i) => parseInt(digits.slice(i, i + 2), 16) / 255);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }

  const rgb = value.match(/^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/);
  if (rgb) {
    const [r, g, b] = rgb.slice(1, 4).map((part) => Number(part) / 255);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  }

  // url(#grad) / var(--x)：静态解析拿不到实际颜色，交给渐变自身的 <stop> 去贡献颜色
  return null;
}

