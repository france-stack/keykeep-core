// 本地联调用：从仓库外的 env 文件加载配置后再启动 Next dev。
// 这样仓库内不出现 .env 文件（secret-scan 会拦），也不影响生产启动方式。
// 默认读取 %TEMP%/keykeep-demo.env，可用 KEYKEEP_ENV_FILE 覆盖。
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const envFile = process.env.KEYKEEP_ENV_FILE || path.join(tmpdir(), "keykeep-demo.env");
const env = { ...process.env };
try {
  for (const line of readFileSync(envFile, "utf8").split(/\r?\n/)) {
    const match = /^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/.exec(line);
    if (!match) continue;
    env[match[1]] = match[2].replace(/^["']|["']$/g, "");
  }
} catch {
  console.warn(`[dev] env file not found: ${envFile} (继续用当前环境变量)`);
}

const child = spawn(
  process.execPath,
  ["node_modules/next/dist/bin/next", "dev", "-p", "3000"],
  { cwd: import.meta.dirname, env, stdio: "inherit" },
);
child.on("exit", (code) => process.exit(code ?? 0));
