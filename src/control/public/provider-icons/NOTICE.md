# 供应商图标来源说明

本目录下的 `*.svg` 由 `scripts/fetch-provider-icons.mjs` 自动抓取生成，**请勿手工编辑**；需要更新时重跑该脚本（`node scripts/fetch-provider-icons.mjs`，加 `--offline` 可只用本地缓存离线重跑）。

## 来源原则

1. **优先厂商官网**：直接取该厂商官网/CDN 上的原始 SVG（站标、`safari-pinned-tab`、品牌页 logo，或官网首页内联的品牌 `<svg>`）。
2. 官网只提供 `.ico`/位图（或站内 `*.svg` 路径实际返回整页 HTML）时，退到 **Simple Icons** 或 **Iconify logos** —— 两者都是 CC0 授权的真实品牌 logo 库，本项目不自行绘制任何图标。
3. 两者都拿不到时**不生成文件**，控制台改用字母块（monogram）兜底，不会产生 404。

## 商标声明

所有图标及其中的商标、标识均为**各厂商所有**，这里仅以「标识所列供应商」为目的使用（nominative fair use），不表示任何厂商对本项目的赞助或认可。图标按原样收录（仅做尺寸/外链清理等无损归一化），未改动配色与造型。

## 逐条来源

| id | 来源 URL | 类型 | 底片 |
| --- | --- | --- | --- |
| `aihubmix` | https://aihubmix.com/logo.svg | official | 深色底片 |
| `anthropic` | https://www.anthropic.com/images/icons/safari-pinned-tab.svg | official | 浅色底片 |
| `azure-openai` | https://api.iconify.design/logos/microsoft-azure.svg | iconify | — |
| `baidu-qianfan` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/baidu.svg | simpleicons | 浅色底片 |
| `bailian` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/alibabacloud.svg | simpleicons | 浅色底片 |
| `bedrock` | https://api.iconify.design/logos/aws.svg | iconify | 浅色底片 |
| `cerebras` | https://api.iconify.design/logos/cerebras-icon.svg | iconify | — |
| `clicline` | https://cline.bot/assets/branding/logos/cline-icon-dark.svg | official | 浅色底片 |
| `codex` | https://openai.com/favicon.svg | official | — |
| `command-code` | https://commandcode.ai/favicon/2024/safari-pinned-tab.svg | official | 浅色底片 |
| `deepinfra` | https://deepinfra.com/_next/static/media/footer_logo.b3e9d8d3.svg | official | 深色底片 |
| `deepseek` | https://api-docs.deepseek.com/img/logo.svg | official | 浅色底片 |
| `fireworks` | https://fireworks.ai/icon.svg | official | 深色底片 |
| `gemini` | https://www.gstatic.com/lamda/images/gemini_sparkle_v002_d4735304ff6292a690345.svg | official | — |
| `groq` | https://groq.com/favicon.svg | official | — |
| `huawei-cloud` | https://res-static.hc-cdn.cn/cloudbu-site/china/zh-cn/wangxue/header/logo.svg | official | 浅色底片 |
| `huggingface` | https://huggingface.co/front/assets/huggingface_logo-noborder.svg | official | — |
| `hyperbolic` | https://www.hyperbolic.ai | official | 浅色底片 |
| `kilo-gateway` | https://kilo.ai/favicon/favicon.svg | official | — |
| `kimi` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/kimi.svg | simpleicons | 浅色底片 |
| `kimi-intl` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/kimi.svg | simpleicons | 浅色底片 |
| `longcat` | https://s3plus.meituan.net/aigc-media-resources/longcat/yeqian-logo.svg | official | 深色底片 |
| `minimax` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/minimax.svg | simpleicons | 浅色底片 |
| `minimax-intl` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/minimax.svg | simpleicons | 浅色底片 |
| `mistral` | https://mistral.ai/favicon.svg | official | — |
| `modelscope` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/modelscope.svg | simpleicons | 浅色底片 |
| `nous-portal` | https://nousresearch.com | official | 浅色底片 |
| `novita` | https://novita.ai/logo/logo_small.svg | official | 浅色底片 |
| `nvidia-nim` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/nvidia.svg | simpleicons | 浅色底片 |
| `ollama-cloud` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/ollama.svg | simpleicons | 浅色底片 |
| `openai` | https://openai.com/favicon.svg | official | — |
| `opendesign` | https://open-design.ai/favicon.svg | official | 浅色底片 |
| `openrouter` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/openrouter.svg | simpleicons | 浅色底片 |
| `pipellm` | https://pipellm.ai/assets/favicon-BYj2DJ7X.svg | official | 浅色底片 |
| `qwen` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/qwen.svg | simpleicons | 浅色底片 |
| `reka` | https://www.reka.ai/_framer/framerusercontent.com/images/nMnEtlqkaGP6SJetHeBGwTuD0.svg | official | 浅色底片 |
| `siliconflow` | https://siliconflow.cn/logo-new.svg | official | 浅色底片 |
| `stepfun` | https://www.stepfun.com/step_favicon.svg | official | — |
| `stepfun-intl` | https://platform.stepfun.ai/favicon.svg | official | 浅色底片 |
| `tencent-hunyuan` | https://hunyuan.tencent.com/logo.svg | official | — |
| `together` | https://api.iconify.design/logos/togetherai-icon.svg | iconify | — |
| `upstage` | https://cdn.prod.website-files.com/6743d5190bb2b52f38e99e37/6748713db65988aab4e2dbf7_G.WEB.svg | official | — |
| `vertex` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/googlecloud.svg | simpleicons | 浅色底片 |
| `volcengine-ark` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/bytedance.svg | simpleicons | 浅色底片 |
| `xai` | https://cdn.jsdelivr.net/npm/simple-icons@16/icons/x.svg | simpleicons | 浅色底片 |
| `xiaomi-mimo` | https://platform.xiaomimimo.com/static/max.dc77b7f93e688db3.svg | official | 浅色底片 |
| `yylx` | https://yylx.io/favicon.svg | official | 浅色底片 |
| `zai` | https://z-cdn.chatglm.cn/z-ai/static/logo.svg | official | — |
| `zhipu` | https://zhipuai.cn/logo.svg | official | 浅色底片 |

共 49 个图标：厂商官网 31 个，Simple Icons 14 个，Iconify logos 4 个。

## 选型备注

- `azure-openai`：Azure OpenAI 没有独立 mark，用 Microsoft Azure（其所属平台）的 CC0 图标。
- `baidu-qianfan`：千帆控制台只提供 .ico，站内无 SVG，用百度（其所属公司）的 CC0 图标。
- `bailian`：百炼没有独立品牌 mark，用阿里云（其所属平台）的 CC0 图标。
- `bedrock`：Amazon Bedrock 没有独立 mark，用 AWS（其所属平台）的 CC0 图标。
- `cerebras`：cerebras.ai 只提供 .ico / 位图，站内无 SVG，用 Iconify logos 的 Cerebras mark（CC0）。
- `clicline`：Cline 官方深色版图标（cline.bot 品牌资源）。
- `codex`：Codex 是 OpenAI 的产品线，复用 OpenAI 官方 mark。
- `huawei-cloud`：华为云官网站头 logo（华为云 CDN 原图）。
- `hyperbolic`：取自 hyperbolic.ai 首页内联的方形品牌 mark（官网未提供独立 SVG 文件）。
- `kimi`：kimi.com 只提供 .ico/.png，站内无 SVG，用 Simple Icons 的 Kimi mark。
- `kimi-intl`：国际站无独立 mark，与 kimi 同一个 Kimi 品牌图标。
- `minimax`：minimax.io 只提供 .ico，站内无 SVG，用 Simple Icons 的 MiniMax mark。
- `minimax-intl`：国际站无独立 mark，与 minimax 同一个 MiniMax 品牌图标。
- `modelscope`：modelscope.cn 的 /favicon.svg 返回整页 HTML，站内无 SVG，用 Simple Icons 的 ModelScope mark。
- `nous-portal`：取自 nousresearch.com 首页内联的 Nous 字母标记（官网唯一一份可用的矢量 mark）。
- `nvidia-nim`：build.nvidia.com 站内无 SVG，用 Simple Icons 的 NVIDIA mark。
- `ollama-cloud`：ollama.com 站内无 SVG favicon，用 Simple Icons 的 Ollama mark。
- `openrouter`：openrouter.ai 站内无 SVG favicon，用 Simple Icons 的 OpenRouter mark。
- `qwen`：qwen.ai 只提供 og:image 位图，站内无 SVG，用 Simple Icons 的 Qwen mark。
- `reka`：reka.ai 官方 wordmark（Framer 站内资源），该站未提供方形 mark。
- `together`：官网只有 wordmark（且是客户 logo 墙），用 Iconify logos 的 Together mark（CC0）。
- `upstage`：upstage.ai 官网页签图标（Webflow CDN 原图）。
- `vertex`：Vertex AI 没有独立品牌 mark，用 Google Cloud（其所属平台）的 CC0 图标。
- `volcengine-ark`：火山引擎站内没有可直接下载的 SVG（/logo.svg 返回整页 HTML），用其母品牌字节跳动的 CC0 图标。
- `xai`：x.ai 对脚本请求返回 403，改用它自己的 X 品牌 mark（Simple Icons CC0）。
- `xiaomi-mimo`：MiMo 官网首页的官方品牌 SVG（platform.xiaomimimo.com 静态资源）。

## 底片（背景对比度补偿）

图标一律保留厂商原件、**不改任何像素**。个别 logo 与卡片底色接近（纯黑 logo 放在深色卡片上、纯白 logo 放在浅色卡片上），前端会在它下面垫一层固定色的圆角底片，两种主题下都能看清。

判定来源（2026-11-10 两轮实测）：

1. 64×64 画布上量每个图标的墨量（alpha > 40 计为墨，墨色亮度 < 60 记黑、> 180 记白），得到纯黑/纯白名单；
2. 按真实展示尺寸（容器 30px、图标 18px）渲染，再把每个图标合成到两种底片色上，算墨像素相对底片色的 WCAG 对比度 —— 用来抓「亮度看着还行、实际整片糊在底色里」的漏网条目（如 bedrock 的深蓝 aws 字标）。

新增条目没有实测数据时，退回按 SVG 里的 fill/stroke/stop-color 颜色静态度量。

- **浅色底片**（深色 logo，共 31 个）：`anthropic`、`baidu-qianfan`、`bailian`、`bedrock`、`clicline`、`command-code`、`deepseek`、`huawei-cloud`、`hyperbolic`、`kimi`、`kimi-intl`、`minimax`、`minimax-intl`、`modelscope`、`nous-portal`、`novita`、`nvidia-nim`、`ollama-cloud`、`opendesign`、`openrouter`、`pipellm`、`qwen`、`reka`、`siliconflow`、`stepfun-intl`、`vertex`、`volcengine-ark`、`xai`、`xiaomi-mimo`、`yylx`、`zhipu`
- **深色底片**（浅色 logo，共 4 个）：`aihubmix`、`deepinfra`、`fireworks`、`longcat`

## 未收录（控制台用字母块兜底）

| id | 原因 |
| --- | --- |
| `302ai` | 302.ai 的 /img/logo.svg 整个文件是一张内嵌位图（`<image>`），去掉位图后没有任何矢量图形，故跳过。 |
| `baichuan` | baichuan-ai.com 只提供 favicon.ico 与 PNG，站内无 SVG，CC0 库也未收录。 |
| `cherryin` | open.cherryin.ai 只提供 PNG（favicon.ico / logo.png），站内无 SVG，CC0 库也未收录。 |
| `iflytek-spark` | 讯飞星火/开放平台只提供 .ico 与位图，站内无 SVG，CC0 库也未收录。 |
| `sambanova` | 官网 sambanova.ai 只有 PNG favicon 与客户 logo，站内无自有 SVG，CC0 库也未收录。 |
