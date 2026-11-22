import { describe, expect, it } from "vitest";

import { flattenCatalog, toCents, versionFor, type ModelsDevCatalog } from "./model-catalog";

// 三家的真实条目（取自 2026-11-05 的 https://models.dev/api.json，字段截断到用到的部分）。
// 只做解析与换算的单元测试，不提交 5.9 MB 全量快照。
const FIXTURE: ModelsDevCatalog = {
  deepseek: {
    id: "deepseek",
    name: "DeepSeek",
    api: "https://api.deepseek.com",
    models: {
      "deepseek-chat": {
        id: "deepseek-chat",
        name: "DeepSeek Chat",
        limit: { context: 128000, output: 8192 },
        cost: { input: 0.66, output: 1.98, cache_read: 0.022 },
        modalities: { input: ["text"], output: ["text"] },
      },
      "deepseek-reasoner": {
        id: "deepseek-reasoner",
        name: "DeepSeek Reasoner",
        limit: { context: 128000, output: 16384 },
        cost: { input: 0.66, output: 1.98 },
        reasoning: true,
        reasoning_options: [{ type: "effort", values: ["low", "medium", "high"] }],
      },
    },
  },
  "zhipuai-coding-plan": {
    id: "zhipuai-coding-plan",
    name: "Zhipu AI Coding Plan",
    models: {
      "glm-5.3": {
        id: "glm-5.3",
        name: "GLM-5.3",
        limit: { context: 200000, output: 65536 },
        cost: {
          input: 0.1494,
          output: 0.44819,
          cache_read: 0.02988,
          cache_write: 0,
          tiers: [{ input: 0.29879, output: 0.89637, cache_read: 0.05976, cache_write: 0, tier: { type: "context", size: 32000 } }],
        } as unknown as { input: number; output: number },
      },
    },
  },
  // 只出图、不给价的供应商：必须进目录但不能进价格表。
  "image-only": {
    id: "image-only",
    name: "Image Only",
    models: {
      "img-1": {
        id: "img-1",
        name: "Image One",
        modalities: { input: ["text"], output: ["image"] },
      },
    },
  },
};

describe("model catalog parsing", () => {
  it("价格换算成整数分 / 百万 token", () => {
    expect(toCents(0.66)).toBe(66n);
    expect(toCents(1.98)).toBe(198n);
    expect(toCents(0.022)).toBe(2n); // 2.2 分四舍五入到 2
    expect(toCents(0)).toBe(0n);
    expect(toCents(undefined)).toBeNull();
    expect(toCents(-1)).toBeNull();
    expect(toCents(Number.NaN)).toBeNull();
    // 0.1494 → 14.94 分 → 15
    expect(toCents(0.1494)).toBe(15n);
  });

  it("版本号按 UTC 日期，同一天重复同步是同一版", () => {
    expect(versionFor(new Date("2026-11-05T23:59:00Z"))).toBe("models.dev-20261105");
    expect(versionFor(new Date("2026-11-05T00:00:00Z"))).toBe("models.dev-20261105");
    expect(versionFor(new Date("2026-11-06T00:00:00Z"))).toBe("models.dev-20261106");
  });

  it("摊平目录：模型行与价格行分开，无价模型只进目录", () => {
    const { models, prices } = flattenCatalog(FIXTURE);
    expect(models).toHaveLength(4);
    expect(prices).toHaveLength(3);

    const chat = models.find((row) => row.modelId === "deepseek-chat");
    expect(chat).toMatchObject({
      provider: "deepseek",
      displayName: "DeepSeek Chat",
      contextLength: 128000,
      maxOutputTokens: 8192,
      type: "chat",
      pricingPromptCents: 66n,
      pricingCompletionCents: 198n,
      pricingCacheReadCents: 2n,
    });

    const reasoner = models.find((row) => row.modelId === "deepseek-reasoner");
    expect(reasoner?.reasoningLevels).toEqual(["low", "medium", "high"]);

    const image = models.find((row) => row.modelId === "img-1");
    expect(image?.type).toBe("image");
    expect(prices.some((row) => row.modelId === "img-1")).toBe(false);
  });

  it("tiers 不参与换算，只取基础价", () => {
    const { prices } = flattenCatalog(FIXTURE);
    const glm = prices.find((row) => row.modelId === "glm-5.3");
    expect(glm).toMatchObject({
      promptPerM: 15n,
      completionPerM: 45n,
      cacheReadPerM: 3n,
      cacheWritePerM: 0n,
    });
  });

  it("空目录与畸形条目不抛错", () => {
    expect(flattenCatalog({})).toEqual({ models: [], prices: [] });
    const broken = { weird: { models: null } } as unknown as ModelsDevCatalog;
    expect(flattenCatalog(broken)).toEqual({ models: [], prices: [] });
  });
});
