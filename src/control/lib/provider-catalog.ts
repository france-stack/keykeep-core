// 供应商目录（纯数据，无密钥）。
// 对齐 Magpie 0.1.2145 的供应商页，但只收录 API Key 型供应商与中转聚合：
// 订阅登录类（Claude/ChatGPT/Copilot/WorkBuddy/Qoder/ZCode 等）按基线 FR-55 不进入生产，故不展示；
// 插件登录型与“合作伙伴/赞助”名单也不在目录内（非官方 API 端点）。

export type Protocol = "anthropic" | "openai" | "gemini" | "vertex" | "xai" | "codex";

export type CatalogGroup = "native" | "compatible" | "aggregator" | "unsupported";

export type CatalogStatus = "ready" | "needs-config" | "unsupported";

/** 一家供应商可能同时讲多种协议，这里列出该条目默认使用的那一种。 */
export type CatalogEntry = {
  id: string;
  name: string;
  group: CatalogGroup;
  protocol: Protocol;
  /** 官方默认端点；needs-config 的条目留空，由用户填写 */
  baseUrl: string;
  /** 同一家的其他协议端点，键是协议名；缺省回退 baseUrl */
  endpoints?: Partial<Record<Protocol, string>>;
  /** 区域变体（国内站 / 国际站这类地理端点差异）；第一项是默认。选中后覆盖 protocol 与 baseUrl */
  regions?: CatalogRegion[];
  /** models.dev 的 provider id（可多个），用于模型目录发现 */
  catalogIds?: string[];
  /**
   * 上游要求把 Key 放在哪：both=两个头都发（部分 Anthropic 兼容中转），
   * x-api-key / bearer=只发一个。缺省按 protocol 推断。
   */
  authHeader?: "both" | "x-api-key" | "bearer";
  /** 是否把 /v1 拼在 base 后面由各协议决定，这里存官方给的根 */
  auth: { header: string; scheme: string };
  docsUrl?: string;
  modelsHint?: string[];
  monogram: { text: string; tone: string };
  status: CatalogStatus;
  note?: string;
};

/**
 * 区域变体：同一家供应商在不同地理位置的端点。
 * 只收录真实的地理/站点差异（国内站 vs 国际站），不收录计费套餐（Coding Plan / Token Plan 等）——
 * 老板买了套餐会自己填 Key 和地址，目录里不再单独开条目。
 */
export type CatalogRegion = {
  id: string;
  name: string;
  protocol: Protocol;
  baseUrl: string;
  endpoints?: Partial<Record<Protocol, string>>;
  modelsHint?: string[];
  docsUrl?: string;
  keysUrl?: string;
};

export const GROUP_LABEL: Record<CatalogGroup, string> = {
  native: "原生协议",
  compatible: "厂商 API",
  aggregator: "中转与聚合",
  unsupported: "暂不支持",
};

const bearer = { header: "Authorization", scheme: "Bearer" };
const xApiKey = { header: "x-api-key", scheme: "" };
const googleKey = { header: "x-goog-api-key", scheme: "" };

export const PROVIDER_CATALOG: CatalogEntry[] = [
  // ── 协议原生（SDK 有独立配置段） ─────────────────────────
  {
    id: "anthropic",
    name: "Anthropic",
    group: "native",
    protocol: "anthropic",
    baseUrl: "https://api.anthropic.com",
    auth: xApiKey,
    docsUrl: "https://docs.anthropic.com",
    modelsHint: ["claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"],
    monogram: { text: "A", tone: "#d97757" },
    status: "ready",
  },
  {
    id: "openai",
    name: "OpenAI",
    group: "native",
    protocol: "openai",
    baseUrl: "https://api.openai.com/v1",
    auth: bearer,
    docsUrl: "https://platform.openai.com/docs",
    modelsHint: ["gpt-5.1", "gpt-5-mini", "o4-mini"],
    monogram: { text: "O", tone: "#10a37f" },
    status: "ready",
  },
  {
    id: "gemini",
    name: "Google Gemini",
    group: "native",
    protocol: "gemini",
    baseUrl: "https://generativelanguage.googleapis.com",
    auth: googleKey,
    docsUrl: "https://ai.google.dev/gemini-api/docs",
    modelsHint: ["gemini-2.5-pro", "gemini-2.5-flash"],
    monogram: { text: "G", tone: "#4285f4" },
    status: "ready",
  },
  {
    id: "vertex",
    name: "Vertex AI",
    group: "native",
    protocol: "vertex",
    baseUrl: "https://aiplatform.googleapis.com",
    auth: googleKey,
    docsUrl: "https://cloud.google.com/vertex-ai/docs",
    note: "仅支持 API Key 模式；暂不支持服务账号 JSON",
    monogram: { text: "V", tone: "#34a853" },
    status: "ready",
  },
  {
    id: "xai",
    name: "xAI",
    group: "native",
    protocol: "xai",
    baseUrl: "https://api.x.ai/v1",
    auth: bearer,
    docsUrl: "https://docs.x.ai",
    modelsHint: ["grok-4", "grok-4-fast"],
    monogram: { text: "X", tone: "#1c1c21" },
    status: "ready",
  },
  {
    id: "codex",
    name: "Codex",
    group: "native",
    protocol: "codex",
    baseUrl: "https://api.openai.com/v1",
    auth: bearer,
    docsUrl: "https://platform.openai.com/docs",
    note: "仅支持 API Key 模式；不包含 Codex 订阅登录",
    monogram: { text: "C", tone: "#0d0d0d" },
    status: "ready",
  },

  // ── OpenAI 兼容厂商 ─────────────────────────────────────
  {
    id: "deepseek",
    name: "DeepSeek",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.deepseek.com",
    endpoints: {
      openai: "https://api.deepseek.com",
      anthropic: "https://api.deepseek.com/anthropic",
    },
    catalogIds: ["deepseek"],
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://api-docs.deepseek.com",
    modelsHint: ["deepseek-chat", "deepseek-reasoner"],
    monogram: { text: "D", tone: "#4d6bfe" },
    status: "ready",
  },
  {
    id: "kimi",
    name: "Kimi",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.moonshot.cn/v1",
    catalogIds: ["moonshotai"],
    auth: bearer,
    docsUrl: "https://platform.moonshot.cn/docs",
    modelsHint: ["kimi-k2", "moonshot-v1-128k"],
    monogram: { text: "K", tone: "#111111" },
    status: "ready",
  },
  {
    id: "kimi-intl",
    name: "Kimi（国际站）",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.moonshot.ai/v1",
    catalogIds: ["moonshotai"],
    auth: bearer,
    docsUrl: "https://platform.moonshot.ai/docs",
    monogram: { text: "KI", tone: "#343a40" },
    status: "ready",
  },
  {
    id: "zhipu",
    name: "智谱 GLM",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://open.bigmodel.cn/api/paas/v4",
    endpoints: {
      openai: "https://open.bigmodel.cn/api/paas/v4",
      anthropic: "https://open.bigmodel.cn/api/anthropic",
    },
    catalogIds: ["zhipuai"],
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://open.bigmodel.cn/dev/api",
    modelsHint: ["glm-4.6", "glm-4-flash"],
    monogram: { text: "Z", tone: "#3b5bdb" },
    status: "ready",
  },
  {
    id: "zai",
    name: "智谱 GLM（国际站）",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.z.ai/api/paas/v4",
    endpoints: {
      openai: "https://api.z.ai/api/paas/v4",
      anthropic: "https://api.z.ai/api/anthropic",
    },
    catalogIds: ["zhipuai"],
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://docs.z.ai",
    monogram: { text: "Z", tone: "#7048e8" },
    status: "ready",
  },
  {
    id: "minimax",
    name: "MiniMax",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.minimax.chat/v1",
    catalogIds: ["minimax"],
    auth: bearer,
    docsUrl: "https://platform.minimaxi.com/document",
    modelsHint: ["MiniMax-M2", "abab6.5s-chat"],
    monogram: { text: "M", tone: "#e64980" },
    status: "ready",
  },
  {
    id: "minimax-intl",
    name: "MiniMax（国际站）",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.minimax.io/v1",
    catalogIds: ["minimax"],
    auth: bearer,
    docsUrl: "https://platform.minimaxi.com/document",
    monogram: { text: "MI", tone: "#d6336c" },
    status: "ready",
  },
  {
    id: "stepfun",
    name: "阶跃星辰",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.stepfun.com/v1",
    catalogIds: ["stepfun-ai"],
    auth: bearer,
    docsUrl: "https://platform.stepfun.com/docs",
    modelsHint: ["step-2-16k", "step-1v-8k"],
    monogram: { text: "S", tone: "#1971c2" },
    status: "ready",
  },
  {
    id: "stepfun-intl",
    name: "阶跃星辰（国际站）",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.stepfun.ai/v1",
    catalogIds: ["stepfun-ai"],
    auth: bearer,
    docsUrl: "https://platform.stepfun.com/docs",
    monogram: { text: "SI", tone: "#1864ab" },
    status: "ready",
  },
  {
    id: "xiaomi-mimo",
    name: "小米 MiMo",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.xiaomimimo.com/v1",
    endpoints: {
      openai: "https://api.xiaomimimo.com/v1",
      anthropic: "https://api.xiaomimimo.com/anthropic",
    },
    catalogIds: ["xiaomi"],
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://platform.xiaomimimo.com",
    monogram: { text: "MI", tone: "#ff6900" },
    status: "ready",
  },
  {
    id: "baidu-qianfan",
    name: "百度千帆",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://qianfan.baidubce.com/v2",
    auth: bearer,
    docsUrl: "https://cloud.baidu.com/doc/qianfan-api",
    modelsHint: ["ernie-4.5-turbo-128k"],
    monogram: { text: "B", tone: "#2932e1" },
    status: "ready",
  },
  {
    id: "tencent-hunyuan",
    name: "腾讯混元",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.hunyuan.cloud.tencent.com/v1",
    catalogIds: ["tencent"],
    auth: bearer,
    docsUrl: "https://cloud.tencent.com/document/product/1729",
    modelsHint: ["hunyuan-turbos-latest"],
    monogram: { text: "T", tone: "#0052d9" },
    status: "ready",
  },
  {
    id: "huawei-cloud",
    name: "华为云 MaaS",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.modelarts-maas.com/openai/v1",
    endpoints: { anthropic: "https://api.modelarts-maas.com/anthropic" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://support.huaweicloud.com/modelarts",
    note: "ModelArts MaaS；按量端点需要在 URL 里带项目 id",
    monogram: { text: "H", tone: "#cf0a2c" },
    status: "ready",
  },
  {
    id: "volcengine-ark",
    name: "火山方舟",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://ark.cn-beijing.volces.com/api/v3",
    catalogIds: ["volcengine"],
    auth: bearer,
    docsUrl: "https://www.volcengine.com/docs/82379",
    modelsHint: ["doubao-seed-1.6", "deepseek-v3.2"],
    monogram: { text: "V", tone: "#1a73e8" },
    status: "ready",
  },
  {
    id: "qwen",
    name: "通义千问（DashScope）",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1",
    catalogIds: ["alibaba"],
    auth: bearer,
    docsUrl: "https://help.aliyun.com/zh/model-studio",
    modelsHint: ["qwen3-max", "qwen-plus"],
    note: "中国站与国际站是两套账号与 Key",
    regions: [
      {
        id: "cn",
        name: "中国站",
        protocol: "openai",
        baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1",
      },
      {
        id: "intl",
        name: "国际站",
        protocol: "openai",
        baseUrl: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
      },
    ],
    monogram: { text: "Q", tone: "#6f42c1" },
    status: "ready",
  },
  {
    id: "bailian",
    name: "阿里云百炼",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://dashscope.aliyuncs.com/compatible-mode/v1",
    catalogIds: ["alibaba"],
    auth: bearer,
    docsUrl: "https://help.aliyun.com/zh/model-studio",
    monogram: { text: "AL", tone: "#ff6a00" },
    status: "ready",
  },
  {
    id: "mistral",
    name: "Mistral",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.mistral.ai/v1",
    catalogIds: ["mistral"],
    auth: bearer,
    docsUrl: "https://docs.mistral.ai",
    modelsHint: ["mistral-large-latest", "mistral-small-latest"],
    monogram: { text: "M", tone: "#ff7000" },
    status: "ready",
  },
  {
    id: "groq",
    name: "Groq",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.groq.com/openai/v1",
    catalogIds: ["groq"],
    auth: bearer,
    docsUrl: "https://console.groq.com/docs",
    modelsHint: ["llama-3.3-70b-versatile"],
    monogram: { text: "G", tone: "#f55036" },
    status: "ready",
  },
  {
    id: "ollama-cloud",
    name: "Ollama Cloud",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://ollama.com/v1",
    catalogIds: ["ollama-cloud"],
    auth: bearer,
    docsUrl: "https://docs.ollama.com",
    note: "端点为 Ollama 官方文档地址",
    monogram: { text: "OL", tone: "#111111" },
    status: "ready",
  },
  {
    id: "nous-portal",
    name: "Nous Portal",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://inference-api.nousresearch.com/v1",
    auth: bearer,
    docsUrl: "https://nousresearch.com",
    monogram: { text: "N", tone: "#495057" },
    status: "ready",
  },
  {
    id: "sambanova",
    name: "SambaNova",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.sambanova.ai/v1",
    auth: bearer,
    docsUrl: "https://docs.sambanova.ai",
    monogram: { text: "SN", tone: "#e03131" },
    status: "ready",
  },
  {
    id: "cerebras",
    name: "Cerebras",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.cerebras.ai/v1",
    auth: bearer,
    docsUrl: "https://inference-docs.cerebras.ai",
    monogram: { text: "CB", tone: "#f08c00" },
    status: "ready",
  },
  {
    id: "huggingface",
    name: "Hugging Face Router",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://router.huggingface.co/v1",
    auth: bearer,
    docsUrl: "https://huggingface.co/docs/inference-providers",
    monogram: { text: "HF", tone: "#ffd43b" },
    status: "ready",
  },
  {
    id: "upstage",
    name: "Upstage",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.upstage.ai/v1",
    auth: bearer,
    docsUrl: "https://developers.upstage.ai",
    monogram: { text: "UP", tone: "#7048e8" },
    status: "ready",
  },
  {
    id: "reka",
    name: "Reka",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.reka.ai/v1",
    auth: bearer,
    docsUrl: "https://docs.reka.ai",
    monogram: { text: "RK", tone: "#9c36b5" },
    status: "ready",
  },

  // ── 国内补充 ──────────────────────────────────────────
  {
    id: "longcat",
    name: "美团 LongCat",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.longcat.chat/openai/v1",
    auth: bearer,
    docsUrl: "https://longcat.chat",
    monogram: { text: "LC", tone: "#2b8a3e" },
    status: "ready",
  },
  {
    id: "iflytek-spark",
    name: "讯飞星火",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://spark-api-open.xf-yun.com/v1",
    auth: bearer,
    docsUrl: "https://www.xfyun.cn/doc/spark",
    monogram: { text: "XF", tone: "#1971c2" },
    status: "ready",
  },
  {
    id: "baichuan",
    name: "百川智能",
    group: "compatible",
    protocol: "openai",
    baseUrl: "https://api.baichuan-ai.com/v1",
    auth: bearer,
    docsUrl: "https://platform.baichuan-ai.com/docs",
    monogram: { text: "BC", tone: "#c92a2a" },
    status: "ready",
  },

  // ── 中转 / 聚合 ────────────────────────────────────────
  // OpenDesign 是本机实测确认过的 Anthropic 兼容中转：/v1/messages 存在，
  // 无凭据时返回 401 missing_api_key。模型名由上游目录提供，按需填写。
  {
    id: "opendesign",
    name: "OpenDesign",
    group: "aggregator",
    protocol: "anthropic",
    baseUrl: "https://amr-link.open-design.ai",
    auth: bearer,
    modelsHint: ["deepseek-v4.1-flash"],
    monogram: { text: "OD", tone: "#2f6f4f" },
    status: "ready",
  },
  {
    id: "openrouter",
    name: "OpenRouter",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://openrouter.ai/api/v1",
    endpoints: { anthropic: "https://openrouter.ai/api" },
    catalogIds: ["openrouter"],
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://openrouter.ai/docs",
    modelsHint: ["openai/gpt-5.1", "anthropic/claude-sonnet-4.5"],
    monogram: { text: "OR", tone: "#646cff" },
    status: "ready",
  },
  {
    id: "together",
    name: "Together AI",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.together.xyz/v1",
    catalogIds: ["togetherai"],
    auth: bearer,
    docsUrl: "https://docs.together.ai",
    modelsHint: ["meta-llama/Llama-3.3-70B-Instruct-Turbo"],
    monogram: { text: "T", tone: "#0f6fff" },
    status: "ready",
  },
  {
    id: "fireworks",
    name: "Fireworks",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.fireworks.ai/inference/v1",
    catalogIds: ["fireworks-ai"],
    auth: bearer,
    docsUrl: "https://docs.fireworks.ai",
    modelsHint: ["accounts/fireworks/models/deepseek-v3"],
    monogram: { text: "F", tone: "#f03e3e" },
    status: "ready",
  },
  {
    id: "novita",
    name: "Novita AI",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.novita.ai/v3/openai",
    auth: bearer,
    docsUrl: "https://novita.ai/docs",
    monogram: { text: "NV", tone: "#5f3dc4" },
    status: "ready",
  },
  {
    id: "deepinfra",
    name: "DeepInfra",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.deepinfra.com/v1/openai",
    auth: bearer,
    docsUrl: "https://deepinfra.com/docs",
    monogram: { text: "DI", tone: "#1098ad" },
    status: "ready",
  },
  {
    id: "hyperbolic",
    name: "Hyperbolic",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.hyperbolic.xyz/v1",
    auth: bearer,
    docsUrl: "https://docs.hyperbolic.xyz",
    monogram: { text: "HY", tone: "#6741d9" },
    status: "ready",
  },
  {
    id: "siliconflow",
    name: "硅基流动",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.siliconflow.cn/v1",
    catalogIds: ["siliconflow"],
    auth: bearer,
    docsUrl: "https://docs.siliconflow.cn",
    modelsHint: ["deepseek-ai/DeepSeek-V3.2"],
    note: "中国站与国际站是两套账号，余额币种也不同",
    regions: [
      {
        id: "cn",
        name: "中国站",
        protocol: "openai",
        baseUrl: "https://api.siliconflow.cn/v1",
        docsUrl: "https://docs.siliconflow.cn",
      },
      {
        id: "intl",
        name: "国际站",
        protocol: "openai",
        baseUrl: "https://api.siliconflow.com/v1",
        docsUrl: "https://docs.siliconflow.com",
      },
    ],
    monogram: { text: "SF", tone: "#7048e8" },
    status: "ready",
  },
  {
    id: "nvidia-nim",
    name: "NVIDIA NIM",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://integrate.api.nvidia.com/v1",
    catalogIds: ["nvidia"],
    auth: bearer,
    docsUrl: "https://docs.nvidia.com/nim",
    monogram: { text: "NV", tone: "#76b900" },
    status: "ready",
  },
  {
    id: "modelscope",
    name: "魔搭 ModelScope",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api-inference.modelscope.cn/v1",
    catalogIds: ["modelscope"],
    auth: bearer,
    docsUrl: "https://modelscope.cn/docs",
    monogram: { text: "MS", tone: "#4c6ef5" },
    status: "ready",
  },
  {
    id: "aihubmix",
    name: "AiHubMix",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://aihubmix.com/v1",
    endpoints: { anthropic: "https://aihubmix.com" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://docs.aihubmix.com",
    monogram: { text: "AH", tone: "#0c8599" },
    status: "ready",
  },
  {
    id: "kilo-gateway",
    name: "Kilo Gateway",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.kilo.ai/api/openrouter",
    auth: bearer,
    docsUrl: "https://kilo.ai/docs/gateway",
    note: "免费模型无需 Key；填 Kilo 的 Key 才能访问其余模型",
    monogram: { text: "KG", tone: "#495057" },
    status: "ready",
  },
  {
    id: "command-code",
    name: "Command Code",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.commandcode.ai/provider/v1",
    endpoints: { anthropic: "https://api.commandcode.ai/provider" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://commandcode.ai/docs/provider",
    note: "Claude 系列只在 Anthropic 端点上应答",
    monogram: { text: "CC", tone: "#212529" },
    status: "ready",
  },
  {
    id: "pipellm",
    name: "PipeLLM",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.pipellm.ai/openai/v1",
    endpoints: {
      openai: "https://api.pipellm.ai/v1",
      anthropic: "https://api.pipellm.ai",
    },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://console.pipellm.ai",
    note: "GPT 系列走 Responses 端点，Claude 系列走 Messages 端点",
    monogram: { text: "PL", tone: "#1971c2" },
    status: "ready",
  },
  {
    id: "302ai",
    name: "302.AI",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.302.ai/v1",
    endpoints: { anthropic: "https://api.302.ai" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://302.ai",
    monogram: { text: "302", tone: "#f76707" },
    status: "ready",
  },
  {
    id: "cherryin",
    name: "CherryIN",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://open.cherryin.ai/v1",
    endpoints: { anthropic: "https://open.cherryin.ai" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://open.cherryin.ai",
    monogram: { text: "CI", tone: "#e8590c" },
    status: "ready",
  },
  {
    id: "yylx",
    name: "鱼鱼连线 (Yuyu Link)",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://app.yylx.io/v1",
    endpoints: { anthropic: "https://app.yylx.io" },
    auth: bearer,
    authHeader: "bearer",
    docsUrl: "https://yylx.io",
    note: "按 Key 签发站点选择区域",
    regions: [
      {
        id: "auto",
        name: "自动",
        protocol: "openai",
        baseUrl: "https://app.yylx.io/v1",
        endpoints: { anthropic: "https://app.yylx.io" },
      },
      {
        id: "global",
        name: "国际站",
        protocol: "openai",
        baseUrl: "https://global.yylx.io/v1",
        endpoints: { anthropic: "https://global.yylx.io" },
      },
      {
        id: "cn",
        name: "中国内地",
        protocol: "openai",
        baseUrl: "https://cn.yylx.io/v1",
        endpoints: { anthropic: "https://cn.yylx.io" },
      },
    ],
    monogram: { text: "YL", tone: "#0c8599" },
    status: "ready",
  },
  {
    id: "clicline",
    name: "ClinePass",
    group: "aggregator",
    protocol: "openai",
    baseUrl: "https://api.cline.bot/api/v1",
    auth: bearer,
    docsUrl: "https://docs.cline.bot/getting-started/clinepass",
    note: "Cline 的模型订阅服务；只代理该订阅包含的模型",
    monogram: { text: "CL", tone: "#1b6ec2" },
    status: "ready",
  },

  // ── 暂不支持（列出但置灰） ─────────────────────────────
  {
    id: "bedrock",
    name: "Amazon Bedrock",
    group: "unsupported",
    protocol: "openai",
    baseUrl: "",
    auth: bearer,
    note: "需要 SigV4 签名；网关尚未实现该协议",
    monogram: { text: "AW", tone: "#ff9900" },
    status: "unsupported",
  },
  {
    id: "azure-openai",
    name: "Azure OpenAI",
    group: "unsupported",
    protocol: "openai",
    baseUrl: "",
    auth: { header: "api-key", scheme: "" },
    note: "需要 api-version 查询参数与资源端点；暂不支持",
    monogram: { text: "AZ", tone: "#0078d4" },
    status: "unsupported",
  },
];

export function findCatalogEntry(id: string): CatalogEntry | undefined {
  return PROVIDER_CATALOG.find((entry) => entry.id === id);
}

/**
 * 一个条目可选的协议：自身协议 + endpoints 里声明的其他协议。
 * 表单用它渲染协议下拉；只有一项时不显示下拉。
 */
export function protocolsOf(entry: CatalogEntry): Protocol[] {
  const out: Protocol[] = [entry.protocol];
  for (const protocol of Object.keys(entry.endpoints ?? {}) as Protocol[]) {
    if (!out.includes(protocol)) out.push(protocol);
  }
  return out;
}

/**
 * 条目在给定协议、给定区域下的端点。
 * 顺序：区域 endpoints → 区域 baseUrl（协议一致时）→ 条目 endpoints → 条目 baseUrl。
 */
export function endpointFor(entry: CatalogEntry, protocol: Protocol, regionId?: string): string {
  const region = regionId ? entry.regions?.find((item) => item.id === regionId) : undefined;
  const fromRegion = region?.endpoints?.[protocol];
  if (fromRegion) return fromRegion;
  if (region && region.protocol === protocol) return region.baseUrl;
  return entry.endpoints?.[protocol] ?? entry.baseUrl;
}

export function catalogGroups(): Array<{ group: CatalogGroup; entries: CatalogEntry[] }> {
  const order: CatalogGroup[] = ["native", "compatible", "aggregator", "unsupported"];
  return order
    .map((group) => ({ group, entries: PROVIDER_CATALOG.filter((entry) => entry.group === group) }))
    .filter((item) => item.entries.length > 0);
}

export const PROTOCOL_LABEL: Record<Protocol, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI 兼容",
  gemini: "Gemini",
  vertex: "Vertex",
  xai: "xAI",
  codex: "Codex",
};
