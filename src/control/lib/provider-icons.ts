/**
 * 哪些目录条目真的有图标文件。
 *
 * `public/provider-icons/*.svg` 由 `scripts/fetch-provider-icons.mjs` 生成：优先抓厂商官网上的
 * 原始 SVG，官网只有 .ico/位图时退到 Simple Icons / Iconify logos（均为 CC0 的真实品牌 logo），
 * 两者都拿不到就不生成文件 —— 组件此时走字母块（monogram）兜底。
 * 来源与商标声明见 `public/provider-icons/NOTICE.md`。
 *
 * 组件据此决定要不要发 `<img>` 请求 —— 否则每打开一次供应商页，控制台就多出几十条
 * 404，既淹没真实错误，又白跑网络。
 *
 * 这份清单由 `provider-catalog.test.ts` 的 `provider icons` 断言与目录比对：
 * 文件多一个、少一个都会失败。新增/删除图标后重跑脚本即可同步。
 */
export const PROVIDER_ICON_IDS: ReadonlySet<string> = new Set([
  "aihubmix",
  "anthropic",
  "azure-openai",
  "baidu-qianfan",
  "bailian",
  "bedrock",
  "cerebras",
  "clicline",
  "codex",
  "command-code",
  "deepinfra",
  "deepseek",
  "fireworks",
  "gemini",
  "groq",
  "huawei-cloud",
  "huggingface",
  "hyperbolic",
  "kilo-gateway",
  "kimi",
  "kimi-intl",
  "longcat",
  "minimax",
  "minimax-intl",
  "mistral",
  "modelscope",
  "nous-portal",
  "novita",
  "nvidia-nim",
  "ollama-cloud",
  "openai",
  "opendesign",
  "openrouter",
  "pipellm",
  "qwen",
  "reka",
  "siliconflow",
  "stepfun",
  "stepfun-intl",
  "tencent-hunyuan",
  "together",
  "upstage",
  "vertex",
  "volcengine-ark",
  "xai",
  "xiaomi-mimo",
  "yylx",
  "zai",
  "zhipu",
]);

export function hasProviderIcon(id: string): boolean {
  return PROVIDER_ICON_IDS.has(id);
}
