/**
 * 图标底片清单 —— **自动生成，请勿手改**。
 *
 * 由 `scripts/fetch-provider-icons.mjs` 依据 `src/control/public/provider-icons/*.svg` 生成；
 * 要更新就重跑脚本（`node scripts/fetch-provider-icons.mjs`），改动会随图标一起刷新。
 *
 * 背景：图标一律是厂商官网原件，不改任何像素。但有的 logo 与卡片底色撞色 ——
 * 纯黑 logo 放在深色卡片上、纯白 logo 放在浅色卡片上都会看不见。做法是在图标下面垫一层
 * 固定色圆角底片（见 globals.css 的 `.provider-logo--on-light` / `--on-dark`），
 * 两种主题下都能看清。
 *
 * 判定来源（2026-11-10 两轮实测）：
 * 1. 64×64 画布量每个图标的墨量（alpha > 40 计为墨，墨色亮度 < 60 记黑、> 180 记白）；
 * 2. 按真实展示尺寸（容器 30px、图标 18px）渲染，再合成到底片色上算墨像素的 WCAG 对比度，
 *    抓出「亮度看着还行、实际整片糊住」的漏网条目。
 * 没有实测数据的新条目退回按 SVG 里的 fill/stroke/stop-color 颜色静态度量。
 */

/** 需要**浅色**底片的 id（图标本身偏黑，深色卡片上会糊成一团）。 */
export const ICON_ON_LIGHT_TILE: ReadonlySet<string> = new Set([
  "anthropic",
  "baidu-qianfan",
  "bailian",
  "bedrock",
  "clicline",
  "command-code",
  "deepseek",
  "huawei-cloud",
  "hyperbolic",
  "kimi",
  "kimi-intl",
  "minimax",
  "minimax-intl",
  "modelscope",
  "nous-portal",
  "novita",
  "nvidia-nim",
  "ollama-cloud",
  "opendesign",
  "openrouter",
  "pipellm",
  "qwen",
  "reka",
  "siliconflow",
  "stepfun-intl",
  "vertex",
  "volcengine-ark",
  "xai",
  "xiaomi-mimo",
  "yylx",
  "zhipu",
]);

/** 需要**深色**底片的 id（图标本身偏白，浅色卡片上会白底白图）。 */
export const ICON_ON_DARK_TILE: ReadonlySet<string> = new Set([
  "aihubmix",
  "deepinfra",
  "fireworks",
  "longcat",
]);

/** 查某个供应商图标该垫哪种底片；不需要垫返回 `null`。 */
export function iconTileKind(id: string): "light" | "dark" | null {
  if (ICON_ON_LIGHT_TILE.has(id)) return "light";
  if (ICON_ON_DARK_TILE.has(id)) return "dark";
  return null;
}
