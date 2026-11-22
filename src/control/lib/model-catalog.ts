/**
 * FR-11 模型目录发现：从 models.dev 拉取模型清单，落 PG 的 ModelCatalog / ModelPrice。
 *
 * 数据源 https://models.dev/api.json（MIT，anomalyco/models.dev）。这不依赖任何
 * 上游厂商凭据，离线部署时可改 MODEL_CATALOG_SOURCE 指向内网镜像。
 *
 * 三条硬约束：
 * 1. 价格换算成「整数分 / 100 万 token」，不引入浮点；
 * 2. 写 ModelPrice 用 `models.dev-YYYYMMDD` 版本，绝不覆盖既有版本；
 * 3. 拉取失败不动库里的旧目录，也不抛给请求路径。
 *
 * 不落文件缓存：库里的 ModelCatalog / ModelPrice 就是上一版的持久副本，
 * 拉取失败时保留它们即可。这样本模块不碰 node:fs，也不会被 Edge 构建扫描到。
 */

import { prisma } from "./db";

export type ModelsDevModel = {
  id?: string;
  name?: string;
  limit?: { context?: number; input?: number; output?: number };
  cost?: { input?: number; output?: number; cache_read?: number; cache_write?: number };
  modalities?: { input?: string[]; output?: string[] };
  reasoning?: boolean;
  reasoning_options?: Array<{ type?: string; values?: string[] }>;
};

export type ModelsDevProvider = {
  id?: string;
  name?: string;
  api?: string;
  models?: Record<string, ModelsDevModel>;
};

export type ModelsDevCatalog = Record<string, ModelsDevProvider>;

export type SyncSummary = {
  version: string;
  providers: number;
  models: number;
  priced: number;
  source: string;
  fetchedAt: string;
};

const DEFAULT_SOURCE = "https://models.dev/api.json";
const MAX_BYTES = 64 * 1024 * 1024;
const FETCH_TIMEOUT_MS = 30_000;

export function catalogSource(): string {
  return process.env.MODEL_CATALOG_SOURCE?.trim() || DEFAULT_SOURCE;
}

/** 美元 / 百万 token → 整数分 / 百万 token。null 表示上游没给这个价格。 */
export function toCents(usdPerMillion: unknown): bigint | null {
  if (typeof usdPerMillion !== "number" || !Number.isFinite(usdPerMillion) || usdPerMillion < 0) return null;
  return BigInt(Math.round(usdPerMillion * 100));
}

/** 版本号按 UTC 日期，同一天重复同步只会 upsert 同一版，不会堆积。 */
export function versionFor(date: Date): string {
  const stamp = date.toISOString().slice(0, 10).replace(/-/g, "");
  return `models.dev-${stamp}`;
}

function isCatalog(value: unknown): value is ModelsDevCatalog {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

/**
 * 拉取 models.dev。失败就抛错，由调用方保留库里的旧目录 —— 不让一次网络抖动
 * 清空已同步的模型与价格。
 */
export async function fetchModelsDev(): Promise<{ catalog: ModelsDevCatalog; source: string }> {
  const source = catalogSource();
  const response = await fetch(source, {
    headers: { accept: "application/json", "user-agent": "keykeep-model-catalog/1.0" },
    signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
  });
  if (!response.ok) throw new Error(`models.dev responded ${response.status}`);

  const length = Number(response.headers.get("content-length") ?? "0");
  if (length > MAX_BYTES) throw new Error(`models.dev payload too large: ${length} bytes`);

  const payload = await response.text();
  if (payload.length > MAX_BYTES) throw new Error(`models.dev payload too large: ${payload.length} bytes`);

  const parsed: unknown = JSON.parse(payload);
  if (!isCatalog(parsed)) throw new Error("models.dev payload is not a provider map");
  return { catalog: parsed, source };
}

type CatalogRow = {
  provider: string;
  modelId: string;
  displayName: string;
  contextLength: number | null;
  maxInputTokens: number | null;
  maxOutputTokens: number | null;
  pricingPromptCents: bigint;
  pricingCompletionCents: bigint;
  pricingCacheReadCents: bigint;
  type: string;
  reasoningLevels: string[];
};

type PriceRow = {
  provider: string;
  modelId: string;
  promptPerM: bigint;
  completionPerM: bigint;
  cacheReadPerM: bigint;
  cacheWritePerM: bigint;
};

function reasoningLevels(model: ModelsDevModel): string[] {
  const options = Array.isArray(model.reasoning_options) ? model.reasoning_options : [];
  const levels = options.flatMap((option) => (Array.isArray(option?.values) ? option.values : []));
  return [...new Set(levels.filter((value): value is string => typeof value === "string" && value.length > 0))];
}

function modelType(model: ModelsDevModel): string {
  const outputs = model.modalities?.output ?? [];
  if (outputs.includes("image")) return "image";
  if (outputs.includes("video")) return "video";
  if (outputs.includes("audio")) return "audio";
  return "chat";
}

/** 把 models.dev 的目录摊平成写入用的行；纯函数，便于单测。 */
export function flattenCatalog(catalog: ModelsDevCatalog): { models: CatalogRow[]; prices: PriceRow[] } {
  const models: CatalogRow[] = [];
  const prices: PriceRow[] = [];
  for (const providerId of Object.keys(catalog)) {
    const modelsById = catalog[providerId]?.models;
    if (!modelsById || typeof modelsById !== "object") continue;
    for (const modelId of Object.keys(modelsById)) {
      if (!modelId) continue;
      const model = modelsById[modelId] as ModelsDevModel;
      const prompt = toCents(model.cost?.input);
      const completion = toCents(model.cost?.output);
      const cacheRead = toCents(model.cost?.cache_read);
      const cacheWrite = toCents(model.cost?.cache_write);
      models.push({
        provider: providerId,
        modelId,
        displayName: model.name?.trim() || modelId,
        contextLength: model.limit?.context ?? null,
        maxInputTokens: model.limit?.input ?? null,
        maxOutputTokens: model.limit?.output ?? null,
        pricingPromptCents: prompt ?? 0n,
        pricingCompletionCents: completion ?? 0n,
        pricingCacheReadCents: cacheRead ?? 0n,
        type: modelType(model),
        reasoningLevels: reasoningLevels(model),
      });
      if (prompt !== null || completion !== null || cacheRead !== null || cacheWrite !== null) {
        prices.push({
          provider: providerId,
          modelId,
          promptPerM: prompt ?? 0n,
          completionPerM: completion ?? 0n,
          cacheReadPerM: cacheRead ?? 0n,
          cacheWritePerM: cacheWrite ?? 0n,
        });
      }
    }
  }
  return { models, prices };
}

/**
 * 同步一次目录。写库用批量 upsert；ModelPrice 用 `models.dev-<日期>` 版本，
 * `skipDuplicates` 保证已有版本不被覆盖。任何一步失败都不清空既有数据。
 */
export async function syncModelCatalog(): Promise<SyncSummary> {
  const { catalog, source } = await fetchModelsDev();
  const { models, prices } = flattenCatalog(catalog);
  const fetchedAt = new Date();
  const version = versionFor(fetchedAt);

  const CHUNK = 500;
  for (let index = 0; index < models.length; index += CHUNK) {
    const slice = models.slice(index, index + CHUNK);
    await prisma.$transaction(
      slice.map((row) =>
        prisma.modelCatalog.upsert({
          where: { provider_modelId: { provider: row.provider, modelId: row.modelId } },
          create: row,
          update: row,
        }),
      ),
    );
  }

  // 价格只补不覆盖：同一天重复同步走 skipDuplicates，历史价格版本永远保留。
  for (let index = 0; index < prices.length; index += CHUNK) {
    await prisma.modelPrice.createMany({
      data: prices.slice(index, index + CHUNK).map((row) => ({
        ...row,
        version,
        effectiveFrom: fetchedAt,
      })),
      skipDuplicates: true,
    });
  }

  return {
    version,
    providers: Object.keys(catalog).length,
    models: models.length,
    priced: prices.length,
    source,
    fetchedAt: fetchedAt.toISOString(),
  };
}
