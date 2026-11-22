import { hash } from "node:crypto";
import { gzipSync } from "node:zlib";

import { prisma } from "./db";
import { getLicenseStatus } from "./license";

// 诊断包里要记录「这一项有没有配」的环境变量名。
//
// 不含 REDIS_URL：Redis 已从部署栈移除（源码零引用），继续观察一个不存在的
// 变量只会在诊断包里固定输出一行「未设置」，让人误以为少配了一项。
const ENV_NAMES = [
  "DATABASE_URL",
  "KEYKEEP_KEK",
  "SESSION_SECRET",
  "KEYKEEP_AUDIT_CHAIN_KEY",
  "KEYKEEP_GATEWAY_URL",
  "KEYKEEP_MASK_MODE",
  "KEYKEEP_MASK_MODEL_URL",
];

const SECRET_PATTERNS: Array<{ name: string; re: RegExp }> = [
  { name: "openai-key", re: /\bsk-[A-Za-z0-9_-]{20,}\b/g },
  { name: "virtual-key", re: /\bkk_[A-Za-z0-9_-]{20,}\b/g },
  { name: "aws-access-key", re: /\bAKIA[0-9A-Z]{16}\b/g },
  { name: "private-key", re: /-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g },
  { name: "bearer-token", re: /\bBearer\s+[A-Za-z0-9._-]{16,}\b/gi },
];

export type DiagnosticFile = { name: string; content: string };

export type DiagnosticBundle = {
  filename: string;
  bytes: Buffer;
  sha256: string;
  manifest: Record<string, unknown>;
};

function tarHeader(name: string, size: number, mtimeMs: number): Buffer {
  const header = Buffer.alloc(512, 0);
  header.write(name.slice(0, 100), 0, 100, "utf8");
  header.write("0000644\0", 100, 8, "ascii");
  header.write("0000000\0", 108, 8, "ascii");
  header.write("0000000\0", 116, 8, "ascii");
  header.write(size.toString(8).padStart(11, "0") + "\0", 124, 12, "ascii");
  header.write(Math.floor(mtimeMs / 1000).toString(8).padStart(11, "0") + "\0", 136, 12, "ascii");
  header.write("        ", 148, 8, "ascii");
  header.write("0", 156, 1, "ascii");
  header.write("ustar\0", 257, 6, "ascii");
  header.write("00", 263, 2, "ascii");
  let checksum = 0;
  for (const byte of header) checksum += byte;
  header.write(checksum.toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  return header;
}

function buildTar(files: DiagnosticFile[], mtimeMs: number): Buffer {
  const chunks: Buffer[] = [];
  for (const file of files) {
    const data = Buffer.from(file.content, "utf8");
    chunks.push(tarHeader(file.name, data.length, mtimeMs));
    chunks.push(data);
    const padding = (512 - (data.length % 512)) % 512;
    if (padding > 0) chunks.push(Buffer.alloc(padding, 0));
  }
  chunks.push(Buffer.alloc(1024, 0));
  return Buffer.concat(chunks);
}

/** 对包内文本做最后一道秘密扫描；命中则打码并记录，绝不把原值打进包。 */
function scanSecrets(files: DiagnosticFile[]): DiagnosticFile {
  const hits: string[] = [];
  for (const file of files) {
    if (file.name === "secret_scan.txt") continue;
    for (const pattern of SECRET_PATTERNS) {
      const matches = file.content.match(pattern.re);
      if (matches) {
        hits.push(`${file.name}: ${pattern.name} x${matches.length}`);
        file.content = file.content.replace(pattern.re, "[REDACTED]");
      }
    }
  }
  return {
    name: "secret_scan.txt",
    content: hits.length === 0 ? "hits=0\n" : `hits=${hits.length}\n${hits.join("\n")}\n`,
  };
}

export async function buildDiagnostics(): Promise<DiagnosticBundle> {
  const now = new Date();
  const since = new Date(now.getTime() - 24 * 60 * 60 * 1000);

  const [counts, migrations, rules, auditRows, usage, license] = await Promise.all([
    Promise.all([
      prisma.user.count().then((n) => ["User", n] as const),
      prisma.apiKey.count().then((n) => ["ApiKey", n] as const),
      prisma.providerCredential.count().then((n) => ["ProviderCredential", n] as const),
      prisma.usageRecord.count().then((n) => ["UsageRecord", n] as const),
      prisma.budget.count().then((n) => ["Budget", n] as const),
      prisma.auditEvent.count().then((n) => ["AuditEvent", n] as const),
    ]),
    prisma
      .$queryRawUnsafe<Array<{ migration_name: string }>>(
        'SELECT migration_name FROM "_prisma_migrations" WHERE finished_at IS NOT NULL ORDER BY finished_at',
      )
      // 验收库可能由 psql 手工建表，没有 Prisma 迁移表；诊断包不应因此失败。
      .catch(() => []),
    prisma.redactRule.findMany({
      select: { name: true, kind: true, target: true, priority: true, enabled: true },
      orderBy: [{ priority: "asc" }, { name: "asc" }],
    }),
    prisma.auditEvent.groupBy({
      by: ["action"],
      where: { at: { gte: since } },
      _count: { _all: true },
    }),
    prisma.usageRecord.aggregate({
      where: { ts: { gte: since } },
      _count: { _all: true },
      _sum: { inputTokens: true, outputTokens: true, costCents: true },
    }),
    getLicenseStatus().catch(() => null),
  ]);

  const manifest = {
    generatedAt: now.toISOString(),
    version: process.env.KEYKEEP_VERSION ?? "0.1.0",
    maskingMode: process.env.KEYKEEP_MASK_MODE ?? "rules-only",
    environment: ENV_NAMES.map((name) => ({ name, present: Boolean(process.env[name]) })),
    migrations: migrations.map((row) => row.migration_name),
    tables: Object.fromEntries(counts),
    redactRules: rules,
    auditLast24h: auditRows.map((row) => ({ action: row.action, count: row._count._all })),
    usageLast24h: {
      calls: usage._count._all,
      inputTokens: String(usage._sum.inputTokens ?? 0n),
      outputTokens: String(usage._sum.outputTokens ?? 0n),
      costCents: String(usage._sum.costCents ?? 0n),
    },
    license: license
      ? {
          status: license.status,
          licenseId: license.licenseId,
          expiresAt: license.expiresAt,
          machineFingerprint: license.machineFingerprint,
          revocationVersion: license.revocationVersion,
        }
      : null,
  };

  const files: DiagnosticFile[] = [
    {
      name: "README.txt",
      content:
        "KeyKeep diagnostics package.\n" +
        "Contains configuration names and existence only; never values, credentials, virtual keys or prompt bodies.\n",
    },
    { name: "manifest.json", content: JSON.stringify(manifest, null, 2) + "\n" },
  ];
  files.push(scanSecrets(files));

  const tar = buildTar(files, now.getTime());
  const bytes = gzipSync(tar);
  const stamp = now.toISOString().replace(/[:.]/g, "-");
  return {
    filename: `keykeep-diagnostics-${stamp}.tar.gz`,
    bytes,
    sha256: hash("sha256", bytes),
    manifest,
  };
}
