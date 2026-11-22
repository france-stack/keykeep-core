// 桌面工具的报错出口：把原因写 stderr、退码 1，并在此过程中屏蔽凭据。
//
// 为什么单独成文件：这三个脚本（迁移 / 首装种子 / 口令找回）都由 kkdesk 拉起，
// 而 kkdesk 把它们的 stdout+stderr **原样** tee 进 logs\setup.log。脚本手里握着
// 含数据库口令的 DATABASE_URL 和明文管理员口令，任何把连接串或入参抄进 message
// 的异常（URL 解析失败、pg 连接错误、驱动内部断言）都会让口令落进日志——
// 日志目录的可读面比 0600 的 settings.json 宽得多，而且排障时用户会被要求
// 把 logs\ 整个打包发出来。明文口令的唯一载体是首装那份 welcome 文件，
// 日志不能成为第二个载体。
//
// 屏蔽用精确匹配而不是猜格式：口令本来就在本进程的环境变量里，知道值长什么样，
// 逐字替换不会漏。除了整串环境变量，还单独抽出连接串里的口令段——因为报错
// 常常只复述口令本身（"password authentication failed" 类的包装文案），
// 这时候整串 URL 的匹配帮不上忙。

// 本机会话里承载凭据的环境变量。新增变量时同步 tests/unit/
// test_desktop_release_contract.py 里的对账清单，漏了会让这条防线静默失效。
// PGPASSWORD 当前只交给 initdb / postgres.exe，不经这几个脚本，但它和
// DATABASE_URL 的口令段是同一个值：留在这里，将来任何脚本多收到这一个变量
// 都已经被覆盖，而不是等到出事再补。
export const SECRET_ENV_NAMES = [
  "DATABASE_URL",
  "KEYKEEP_DATABASE_URL",
  "PGPASSWORD",
  "KEYKEEP_KEK",
  "KEYKEEP_SEED_ADMIN_PASSWORD",
  "KEYKEEP_RESET_ADMIN_PASSWORD",
];

// 连接串取口令段的变量：口令段单独再屏蔽一次。
const CONNECTION_STRING_ENV_NAMES = ["DATABASE_URL", "KEYKEEP_DATABASE_URL"];

// 短于该长度的值不替换：过短的词极易误伤正常文案，而真正的凭据不会这么短。
const MIN_REDACT_LENGTH = 6;

const USERINFO_PASSWORD = /^[a-z][a-z0-9+.-]*:\/\/[^:/@]*:([^@]*)@/i;

/** 当前进程能接触到的全部明文凭据值，长优先。 */
function secretValues() {
  const values = [];
  for (const name of SECRET_ENV_NAMES) {
    const value = process.env[name];
    if (value) values.push(value);
  }
  for (const name of CONNECTION_STRING_ENV_NAMES) {
    const matched = USERINFO_PASSWORD.exec(process.env[name] ?? "");
    if (!matched) continue;
    values.push(matched[1]);
    try {
      // 连接串里的口令通常是 percent-encoded 形式；报错文案更可能还原样输出。
      values.push(decodeURIComponent(matched[1]));
    } catch {
      // 非法转义序列：原始形式已经收下了，不必为它再造一个值。
    }
  }
  return [...new Set(values.filter((v) => v.length >= MIN_REDACT_LENGTH))].sort(
    (a, b) => b.length - a.length,
  );
}

export function redactSecrets(message) {
  let text = String(message ?? "");
  for (const value of secretValues()) {
    text = text.split(value).join("***");
  }
  // 兜底：口令来自别处（不在这几个环境变量里）的连接串，也遮住口令段。
  return text.replace(/(\b[a-z][a-z0-9+.-]*:\/\/[^\s/@:]+:)[^\s@]+(@)/gi, "$1***$2");
}

/** 作为 `main().catch(fatal)` 的终点：人看到的最后一行必须已经屏蔽过。 */
export function fatal(error) {
  const message = error instanceof Error ? error.message || error.name : error;
  console.error(redactSecrets(message));
  process.exit(1);
}
