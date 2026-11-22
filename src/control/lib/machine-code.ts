import { createHash, hash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";

/** 机器码文件：安装器的标准只读挂载点，路径固定（避免 env → 任意文件读取）。 */
const MACHINE_CODE_FILE = "/etc/keykeep/machine-code";

export type MachineCode = { code: string; source: "env" | "file" | "missing" };

function normalize(raw: string): string {
  const value = raw.trim().toLowerCase();
  if (/^[0-9a-f]{64}$/.test(value)) return value;
  // 非 64 位十六进制时按原值哈希，保证长度稳定；安装器应写入规范值。
  return hash("sha256", value, "utf8");
}

/**
 * 读取部署实例的机器码。
 * 基准公式（主板 UUID + 首个物理网卡 MAC 的哈希）由主机侧脚本计算；
 * 容器只读环境变量或安装器挂载的只读文件，不读取自身容器 MAC/主机名。
 * 机器码来源：KEYKEEP_MACHINE_CODE 环境变量（首选）或固定路径的挂载文件；
 * 此前的 KEYKEEP_MACHINE_CODE_FILE 自定义路径覆盖无任何消费方，已移除。
 */
export function machineCode(): MachineCode {
  const env = process.env.KEYKEEP_MACHINE_CODE;
  if (env && env.trim()) return { code: normalize(env), source: "env" };
  if (existsSync(MACHINE_CODE_FILE)) {
    const raw = readFileSync(MACHINE_CODE_FILE, "utf8");
    if (raw.trim()) return { code: normalize(raw), source: "file" };
  }
  return { code: "", source: "missing" };
}

/** 诊断包/页面只展示短指纹，避免整串机器码进入无关日志。 */
export function machineCodeFingerprint(code: string): string {
  if (!code) return "";
  // 必须 digest("hex")：此前 hash("sha256", code, "utf8") 把 utf8 当输出编码
  // （非法，实际返回 Buffer），slice 后模板插值按 UTF-8 字符串化原始字节
  // → 乱码进 JSON/证据（test_source_encoding 拦截，2026-11-08 实测）。
  return `sha256:${createHash("sha256").update(code, "utf8").digest("hex").slice(0, 16)}`;
}
