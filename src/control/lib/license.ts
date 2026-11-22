import { createPublicKey, verify as verifySignature } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import type { Prisma } from "@prisma/client";

import { recordAudit } from "./audit";
import { prisma } from "./db";
import { publicKeyFor } from "./license-public-key";
import { machineCode, machineCodeFingerprint } from "./machine-code";

const STATE_ID = "current";
const DAY_MS = 24 * 60 * 60 * 1000;
const TRIAL_DAYS = 30;
const EXPIRING_DAYS = 30;
const CLOCK_SKEW_MS = 5 * 60 * 1000;
const CACHE_MS = 5000;

export type Envelope = { alg: string; kid: string; payload: string; sig: string };

export type LicensePayload = {
  license_id: string;
  customer: string;
  machine_code: string;
  issued_at: string;
  expires_at?: string | null;
  version_range?: string;
  max_employees?: number;
  features?: string[];
};

export type RevocationPayload = {
  version: number;
  issued_at: string;
  license_ids?: string[];
  machine_codes?: string[];
};

export type LicenseStatus = {
  status: string;
  writable: boolean;
  schemaReady: boolean;
  licenseId: string | null;
  customer: string | null;
  machineCode: string;
  machineFingerprint: string;
  expiresAt: string | null;
  daysLeft: number | null;
  trialDaysLeft: number | null;
  firstSeenAt: string | null;
  lastSeenAt: string | null;
  maxEmployees: number;
  revocationVersion: number;
  message: string;
};

export class LicenseError extends Error {}

function nowIso(date: Date | null | undefined): string | null {
  return date ? date.toISOString() : null;
}

function daysBetween(later: Date, earlier: Date): number {
  return Math.ceil((later.getTime() - earlier.getTime()) / DAY_MS);
}

function parseDate(value: string | null | undefined): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function parseEnvelope(input: unknown): Envelope {
  const value = typeof input === "string" ? JSON.parse(input) : input;
  if (!value || typeof value !== "object") throw new LicenseError("license envelope must be an object");
  const raw = value as Record<string, unknown>;
  if (raw.alg !== "Ed25519") throw new LicenseError("unsupported alg");
  if (typeof raw.kid !== "string" || !raw.kid) throw new LicenseError("kid is required");
  if (typeof raw.payload !== "string" || !raw.payload) throw new LicenseError("payload is required");
  if (typeof raw.sig !== "string" || !raw.sig) throw new LicenseError("sig is required");
  return { alg: raw.alg, kid: raw.kid, payload: raw.payload, sig: raw.sig };
}

export function verifyEnvelope(
  envelope: Envelope,
  publicKeyBase64: string,
): { valid: boolean; payloadBytes: Buffer } {
  const payloadBytes = Buffer.from(envelope.payload, "base64url");
  try {
    const key = createPublicKey({
      key: Buffer.from(publicKeyBase64, "base64"),
      format: "der",
      type: "spki",
    });
    const valid = verifySignature(null, payloadBytes, key, Buffer.from(envelope.sig, "base64url"));
    return { valid, payloadBytes };
  } catch {
    return { valid: false, payloadBytes };
  }
}

export function parsePayload(bytes: Buffer): LicensePayload {
  const value = JSON.parse(bytes.toString("utf8")) as Record<string, unknown>;
  if (typeof value.license_id !== "string" || !value.license_id) throw new LicenseError("license_id is required");
  if (typeof value.machine_code !== "string" || !value.machine_code) throw new LicenseError("machine_code is required");
  if (typeof value.issued_at !== "string" || !parseDate(value.issued_at)) throw new LicenseError("issued_at is invalid");
  if (value.expires_at !== undefined && value.expires_at !== null && !parseDate(String(value.expires_at))) {
    throw new LicenseError("expires_at is invalid");
  }
  return {
    license_id: value.license_id,
    customer: typeof value.customer === "string" ? value.customer : "",
    machine_code: value.machine_code,
    issued_at: value.issued_at,
    expires_at: typeof value.expires_at === "string" ? value.expires_at : null,
    version_range: typeof value.version_range === "string" ? value.version_range : "*",
    max_employees: Number.isFinite(Number(value.max_employees)) ? Number(value.max_employees) : 0,
    features: Array.isArray(value.features) ? value.features.filter((x): x is string => typeof x === "string") : [],
  };
}

export function versionMatches(range: string | undefined, version: string): boolean {
  if (!range || range === "*") return true;
  const major = Number.parseInt(version.split(".")[0] ?? "", 10);
  if (!Number.isFinite(major)) return false;
  const match = /^(\d+)\.(x|\*)$/.exec(range);
  if (match) return Number.parseInt(match[1], 10) === major;
  return range === version;
}

function isRevoked(
  payload: LicensePayload | null,
  machine: string,
  revocation: RevocationPayload | null,
): boolean {
  if (!revocation) return false;
  const ids = revocation.license_ids ?? [];
  const machines = revocation.machine_codes ?? [];
  if (payload && ids.includes(payload.license_id)) return true;
  if (machine && machines.includes(machine)) return true;
  return false;
}

export type Evaluation = {
  status: string;
  writable: boolean;
  daysLeft: number | null;
  trialDaysLeft: number | null;
  message: string;
};

/** 纯函数：给定已验签的 payload 与当前时间/机器码，返回状态。 */
export function evaluateLicense(
  payload: LicensePayload | null,
  options: {
    now: Date;
    machineCode: string;
    lastSeenAt: Date;
    firstSeenAt: Date;
    revocation: RevocationPayload | null;
    appVersion: string;
    signatureValid: boolean;
  },
): Evaluation {
  const { now, machineCode: machine, lastSeenAt, firstSeenAt, revocation, appVersion } = options;

  if (!options.signatureValid) {
    return { status: "invalid_signature", writable: false, daysLeft: null, trialDaysLeft: null, message: "License signature is invalid." };
  }

  if (payload === null) {
    if (isRevoked(null, machine, revocation)) {
      return { status: "revoked", writable: false, daysLeft: null, trialDaysLeft: null, message: "This machine is revoked." };
    }
    if (now.getTime() < firstSeenAt.getTime() - CLOCK_SKEW_MS || now.getTime() < lastSeenAt.getTime() - CLOCK_SKEW_MS) {
      return { status: "clock_rollback", writable: false, daysLeft: null, trialDaysLeft: null, message: "Clock rollback detected." };
    }
    const trialDaysLeft = TRIAL_DAYS - Math.floor((now.getTime() - firstSeenAt.getTime()) / DAY_MS);
    if (trialDaysLeft <= 0) {
      return { status: "trial_expired", writable: false, daysLeft: null, trialDaysLeft: 0, message: "30-day trial expired." };
    }
    return { status: "missing", writable: true, daysLeft: null, trialDaysLeft, message: `Trial: ${trialDaysLeft} day(s) left.` };
  }

  if (payload.machine_code !== machine) {
    return { status: "machine_mismatch", writable: false, daysLeft: null, trialDaysLeft: null, message: "License is bound to a different machine." };
  }
  if (isRevoked(payload, machine, revocation)) {
    return { status: "revoked", writable: false, daysLeft: null, trialDaysLeft: null, message: "License has been revoked." };
  }
  if (now.getTime() < lastSeenAt.getTime() - CLOCK_SKEW_MS) {
    return { status: "clock_rollback", writable: false, daysLeft: null, trialDaysLeft: null, message: "Clock rollback detected." };
  }
  if (!versionMatches(payload.version_range, appVersion)) {
    return { status: "version_mismatch", writable: false, daysLeft: null, trialDaysLeft: null, message: "License does not cover this version." };
  }
  const expiresAt = parseDate(payload.expires_at);
  if (expiresAt && expiresAt.getTime() <= now.getTime()) {
    return { status: "expired", writable: false, daysLeft: 0, trialDaysLeft: null, message: "License expired." };
  }
  if (expiresAt) {
    const daysLeft = daysBetween(expiresAt, now);
    if (daysLeft <= EXPIRING_DAYS) {
      return { status: "expiring", writable: true, daysLeft, trialDaysLeft: null, message: `License expires in ${daysLeft} day(s).` };
    }
    return { status: "valid", writable: true, daysLeft, trialDaysLeft: null, message: "License valid." };
  }
  return { status: "valid", writable: true, daysLeft: null, trialDaysLeft: null, message: "Permanent license valid." };
}

function readRevocation(value: Prisma.JsonValue | null): RevocationPayload | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const raw = value as Record<string, unknown>;
  return {
    version: Number(raw.version ?? 0),
    issued_at: typeof raw.issued_at === "string" ? raw.issued_at : "",
    license_ids: Array.isArray(raw.license_ids) ? raw.license_ids.filter((x): x is string => typeof x === "string") : [],
    machine_codes: Array.isArray(raw.machine_codes) ? raw.machine_codes.filter((x): x is string => typeof x === "string") : [],
  };
}

function statusForRow(row: {
  payload: string | null;
  sig: string | null;
  kid: string | null;
  status: string;
  licenseId: string | null;
  customer: string | null;
  expiresAt: Date | null;
  features: Prisma.JsonValue | null;
  firstSeenAt: Date;
  lastSeenAt: Date;
  revocation: Prisma.JsonValue | null;
  revocationVersion: number;
}, now: Date, machine: string): LicenseStatus {
  const revocation = readRevocation(row.revocation);
  const base = {
    schemaReady: true,
    licenseId: row.licenseId,
    customer: row.customer,
    machineCode: machine,
    machineFingerprint: machineCodeFingerprint(machine),
    expiresAt: nowIso(row.expiresAt),
    revocationVersion: row.revocationVersion,
    firstSeenAt: nowIso(row.firstSeenAt),
    lastSeenAt: nowIso(row.lastSeenAt),
  };

  if (!row.payload || !row.sig) {
    const result = evaluateLicense(null, {
      now, machineCode: machine, lastSeenAt: row.lastSeenAt, firstSeenAt: row.firstSeenAt,
      revocation, appVersion: process.env.KEYKEEP_VERSION ?? "0.1.0", signatureValid: true,
    });
    return { ...base, ...result, maxEmployees: 0 };
  }

  let payload: LicensePayload | null = null;
  let signatureValid = false;
  try {
    const envelope: Envelope = { alg: "Ed25519", kid: row.kid ?? "v1", payload: row.payload, sig: row.sig };
    const publicKey = publicKeyFor(envelope.kid);
    if (publicKey) {
      const verified = verifyEnvelope(envelope, publicKey);
      signatureValid = verified.valid;
      // 即使签名无效也尝试解析，便于页面展示授权 ID/到期；状态仍以 signatureValid 为准。
      try { payload = parsePayload(verified.payloadBytes); } catch { payload = null; }
    }
  } catch {
    signatureValid = false;
  }

  const result = evaluateLicense(payload, {
    now, machineCode: machine, lastSeenAt: row.lastSeenAt, firstSeenAt: row.firstSeenAt,
    revocation, appVersion: process.env.KEYKEEP_VERSION ?? "0.1.0", signatureValid,
  });
  return {
    ...base,
    ...result,
    licenseId: payload?.license_id ?? row.licenseId,
    customer: payload?.customer ?? row.customer,
    expiresAt: nowIso(parseDate(payload?.expires_at) ?? row.expiresAt),
    maxEmployees: payload?.max_employees ?? 0,
  };
}

let cache: { value: LicenseStatus; at: number } | null = null;

export function invalidateLicenseCache(): void {
  cache = null;
}

type LicenseRow = {
  id: string;
  licenseId: string | null;
  customer: string | null;
  machineCode: string | null;
  payload: string | null;
  sig: string | null;
  kid: string | null;
  expiresAt: Date | null;
  features: Prisma.JsonValue | null;
  status: string;
  firstSeenAt: Date;
  lastSeenAt: Date;
  revocation: Prisma.JsonValue | null;
  revocationVersion: number;
};

/**
 * 直接读 epoch 毫秒，绕开 Prisma pg 适配器的 +8 小时 Date 偏移。
 * 见 docs/implementation/06-known-limits.md 第 4 条。
 */
async function readRow(): Promise<LicenseRow | null> {
  try {
    const rows = await prisma.$queryRaw<Array<Record<string, unknown>>>`
      SELECT id, "licenseId", customer, "machineCode", payload, sig, kid,
             extract(epoch from "expiresAt") * 1000 AS "expiresAtMs",
             features, status,
             extract(epoch from "firstSeenAt") * 1000 AS "firstSeenAtMs",
             extract(epoch from "lastSeenAt") * 1000 AS "lastSeenAtMs",
             revocation, "revocationVersion"
        FROM "LicenseState"
       WHERE id = ${STATE_ID}
       LIMIT 1`;
    const row = rows[0];
    if (!row) return null;
    const toDate = (value: unknown): Date | null => {
      if (value === null || value === undefined) return null;
      const ms = Number(value);
      return Number.isFinite(ms) ? new Date(ms) : null;
    };
    return {
      id: String(row.id),
      licenseId: (row.licenseId as string | null) ?? null,
      customer: (row.customer as string | null) ?? null,
      machineCode: (row.machineCode as string | null) ?? null,
      payload: (row.payload as string | null) ?? null,
      sig: (row.sig as string | null) ?? null,
      kid: (row.kid as string | null) ?? null,
      expiresAt: toDate(row.expiresAtMs),
      features: (row.features as Prisma.JsonValue | null) ?? null,
      status: String(row.status ?? "missing"),
      firstSeenAt: toDate(row.firstSeenAtMs) ?? new Date(),
      lastSeenAt: toDate(row.lastSeenAtMs) ?? new Date(),
      revocation: (row.revocation as Prisma.JsonValue | null) ?? null,
      revocationVersion: Number(row.revocationVersion ?? 0),
    };
  } catch {
    return null;
  }
}

async function ensureRow(now: Date) {
  try {
    return await prisma.licenseState.upsert({
      where: { id: STATE_ID },
      update: {},
      create: { id: STATE_ID, firstSeenAt: now, lastSeenAt: now, status: "missing" },
    });
  } catch {
    return null;
  }
}

async function importFileIfNeeded(): Promise<void> {
  const file = process.env.KEYKEEP_LICENSE_FILE;
  if (!file || !existsSync(file)) return;
  const existing = await readRow().catch(() => null);
  if (existing) return;
  try {
    const text = readFileSync(file, "utf8");
    await importLicense(text);
  } catch {
    // 启动导入失败不阻塞服务；管理员可在 /license 手动导入。
  }
}

export async function getLicenseStatus(force = false): Promise<LicenseStatus> {
  const now = new Date();
  const machine = machineCode().code;
  if (!force && cache && Date.now() - cache.at < CACHE_MS) return cache.value;

  await importFileIfNeeded();
  let row = await readRow();
  if (!row) {
    await ensureRow(now);
    row = await readRow();
  }

  if (!row) {
    const result = evaluateLicense(null, {
      now, machineCode: machine, lastSeenAt: now, firstSeenAt: now,
      revocation: null, appVersion: process.env.KEYKEEP_VERSION ?? "0.1.0", signatureValid: true,
    });
    const value: LicenseStatus = {
      ...result, schemaReady: false, licenseId: null, customer: null, machineCode: machine,
      machineFingerprint: machineCodeFingerprint(machine), expiresAt: null, daysLeft: null,
      trialDaysLeft: result.trialDaysLeft, firstSeenAt: nowIso(now), lastSeenAt: nowIso(now),
      maxEmployees: 0, revocationVersion: 0,
    };
    cache = { value, at: Date.now() };
    return value;
  }

  const nextLastSeen = row.lastSeenAt.getTime() > now.getTime() ? row.lastSeenAt : now;
  const status = statusForRow(row, now, machine);
  const clockRolledBack = status.status === "clock_rollback";
  try {
    await prisma.licenseState.update({
      where: { id: STATE_ID },
      data: {
        status: status.status,
        lastSeenAt: clockRolledBack ? row.lastSeenAt : nextLastSeen,
      },
    });
  } catch {
    // 状态读取不应因写回失败而阻断；页面仍返回计算结果。
  }
  if (status.status === "clock_rollback" && row.status !== "clock_rollback") {
    void recordAudit({
      action: "license.clock_rollback",
      detail: { status: status.status, lastSeenAt: row.lastSeenAt.toISOString() },
    });
  }
  if (status.status === "invalid_signature" && row.status !== "invalid_signature") {
    void recordAudit({
      action: "license.invalid_signature",
      detail: { licenseId: status.licenseId },
    });
  }
  cache = { value: status, at: Date.now() };
  return status;
}

export async function importLicense(input: unknown): Promise<LicenseStatus> {
  const now = new Date();
  const machine = machineCode().code;
  let envelope: Envelope;
  try {
    envelope = parseEnvelope(input);
  } catch (error) {
    throw new LicenseError(error instanceof Error ? error.message : "invalid envelope");
  }
  const publicKey = publicKeyFor(envelope.kid);
  if (!publicKey) throw new LicenseError("unknown kid or public key not configured");
  const verified = verifyEnvelope(envelope, publicKey);
  const payload = verified.valid ? parsePayload(verified.payloadBytes) : null;
  const expiresAt = payload ? parseDate(payload.expires_at) : null;

  await prisma.licenseState.upsert({
    where: { id: STATE_ID },
    update: {
      licenseId: payload?.license_id ?? null,
      customer: payload?.customer ?? null,
      machineCode: payload?.machine_code ?? null,
      payload: envelope.payload,
      sig: envelope.sig,
      kid: envelope.kid,
      expiresAt,
      features: (payload?.features ?? []) as Prisma.InputJsonValue,
      importedAt: now,
    },
    create: {
      id: STATE_ID,
      licenseId: payload?.license_id ?? null,
      customer: payload?.customer ?? null,
      machineCode: payload?.machine_code ?? null,
      payload: envelope.payload,
      sig: envelope.sig,
      kid: envelope.kid,
      expiresAt,
      features: (payload?.features ?? []) as Prisma.InputJsonValue,
      importedAt: now,
      firstSeenAt: now,
      lastSeenAt: now,
      status: verified.valid ? "valid" : "invalid_signature",
    },
  });
  const row = await readRow();
  if (!row) throw new LicenseError("license state is unavailable");

  const status = statusForRow(row, now, machine);
  const nextLastSeen = row.lastSeenAt.getTime() > now.getTime() ? row.lastSeenAt : now;
  await prisma.licenseState.update({ where: { id: STATE_ID }, data: { status: status.status, lastSeenAt: nextLastSeen } });
  cache = null;
  if (!verified.valid) {
    void recordAudit({ action: "license.invalid_signature", detail: { licenseId: payload?.license_id ?? null } });
  }
  return status;
}

export async function importRevocations(input: unknown): Promise<LicenseStatus> {
  const now = new Date();
  const machine = machineCode().code;
  const envelope = parseEnvelope(input);
  const publicKey = publicKeyFor(envelope.kid);
  if (!publicKey) throw new LicenseError("unknown kid or public key not configured");
  const verified = verifyEnvelope(envelope, publicKey);
  if (!verified.valid) throw new LicenseError("invalid revocation signature");
  const payload = JSON.parse(verified.payloadBytes.toString("utf8")) as RevocationPayload;
  if (!Number.isInteger(Number(payload.version)) || Number(payload.version) < 0 || Number(payload.version) > 2147483647) {
    throw new LicenseError("revocation version must be a 32-bit non-negative integer");
  }

  await prisma.licenseState.upsert({
    where: { id: STATE_ID },
    update: {
      revocation: payload as unknown as Prisma.InputJsonValue,
      revocationVersion: Number(payload.version),
    },
    create: {
      id: STATE_ID,
      firstSeenAt: now,
      lastSeenAt: now,
      status: "missing",
      revocation: payload as unknown as Prisma.InputJsonValue,
      revocationVersion: Number(payload.version),
    },
  });
  const row = await readRow();
  if (!row) throw new LicenseError("license state is unavailable");
  const status = statusForRow(row, now, machine);
  const nextLastSeen = row.lastSeenAt.getTime() > now.getTime() ? row.lastSeenAt : now;
  await prisma.licenseState.update({ where: { id: STATE_ID }, data: { status: status.status, lastSeenAt: nextLastSeen } });
  cache = null;
  return status;
}

export function statusLabel(status: string): string {
  const labels: Record<string, string> = {
    valid: "Valid",
    expiring: "Expiring soon",
    expired: "Expired",
    machine_mismatch: "Machine mismatch",
    invalid_signature: "Invalid signature",
    revoked: "Revoked",
    clock_rollback: "Clock rollback",
    missing: "Trial",
    trial_expired: "Trial expired",
    version_mismatch: "Version mismatch",
  };
  return labels[status] ?? status;
}
