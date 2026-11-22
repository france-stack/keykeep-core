import { generateKeyPairSync, sign } from "node:crypto";
import { describe, expect, it } from "vitest";

import {
  evaluateLicense,
  parsePayload,
  verifyEnvelope,
  versionMatches,
  type Envelope,
  type LicensePayload,
} from "./license";

function keypair() {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  return {
    publicKey: publicKey.export({ type: "spki", format: "der" }).toString("base64"),
    privateKey,
  };
}

function signEnvelope(payload: LicensePayload, privateKey: ReturnType<typeof keypair>["privateKey"]): Envelope {
  const bytes = Buffer.from(JSON.stringify(payload), "utf8");
  return {
    alg: "Ed25519",
    kid: "v1",
    payload: bytes.toString("base64url"),
    sig: sign(null, bytes, privateKey).toString("base64url"),
  };
}

const basePayload: LicensePayload = {
  license_id: "lic-1",
  customer: "Acme",
  machine_code: "a".repeat(64),
  issued_at: "2026-01-01T00:00:00.000Z",
  expires_at: null,
  version_range: "0.x",
  max_employees: 10,
  features: [],
};

const now = new Date("2026-06-01T00:00:00.000Z");
const firstSeen = new Date("2026-01-01T00:00:00.000Z");

function evaluate(payload: LicensePayload | null, overrides: Partial<Parameters<typeof evaluateLicense>[1]> = {}) {
  return evaluateLicense(payload, {
    now,
    machineCode: "a".repeat(64),
    lastSeenAt: now,
    firstSeenAt: firstSeen,
    revocation: null,
    appVersion: "0.1.0",
    signatureValid: true,
    ...overrides,
  });
}

describe("license envelope", () => {
  it("verifies a valid envelope and rejects a tampered signature", () => {
    const { publicKey, privateKey } = keypair();
    const envelope = signEnvelope(basePayload, privateKey);
    expect(verifyEnvelope(envelope, publicKey).valid).toBe(true);

    const tampered = { ...envelope, sig: envelope.sig.slice(0, -4) + "AAAA" };
    expect(verifyEnvelope(tampered, publicKey).valid).toBe(false);
  });

  it("parses payload fields", () => {
    const { privateKey } = keypair();
    const envelope = signEnvelope(basePayload, privateKey);
    const payload = parsePayload(Buffer.from(envelope.payload, "base64url"));
    expect(payload.license_id).toBe("lic-1");
    expect(payload.max_employees).toBe(10);
  });

  it("rejects a signature from a different key", () => {
    const { privateKey } = keypair();
    const other = keypair();
    const envelope = signEnvelope(basePayload, privateKey);
    expect(verifyEnvelope(envelope, other.publicKey).valid).toBe(false);
  });
});

describe("license state machine", () => {
  it("valid permanent", () => {
    expect(evaluate(basePayload).status).toBe("valid");
    expect(evaluate(basePayload).writable).toBe(true);
  });

  it("expiring within 30 days", () => {
    const payload = { ...basePayload, expires_at: "2026-06-20T00:00:00.000Z" };
    const status = evaluate(payload);
    expect(status.status).toBe("expiring");
    expect(status.writable).toBe(true);
  });

  it("expired", () => {
    const payload = { ...basePayload, expires_at: "2026-05-01T00:00:00.000Z" };
    expect(evaluate(payload).status).toBe("expired");
    expect(evaluate(payload).writable).toBe(false);
  });

  it("machine mismatch", () => {
    const status = evaluate(basePayload, { machineCode: "b".repeat(64) });
    expect(status.status).toBe("machine_mismatch");
    expect(status.writable).toBe(false);
  });

  it("invalid signature", () => {
    expect(evaluate(basePayload, { signatureValid: false }).status).toBe("invalid_signature");
  });

  it("revoked by license id or machine code", () => {
    expect(
      evaluate(basePayload, { revocation: { version: 1, issued_at: "", license_ids: ["lic-1"] } }).status,
    ).toBe("revoked");
    expect(
      evaluate(basePayload, {
        revocation: { version: 1, issued_at: "", machine_codes: ["a".repeat(64)] },
      }).status,
    ).toBe("revoked");
  });

  it("clock rollback", () => {
    const future = new Date("2026-07-01T00:00:00.000Z");
    expect(evaluate(basePayload, { lastSeenAt: future }).status).toBe("clock_rollback");
  });

  it("missing license is a 30-day trial", () => {
    const start = new Date("2026-05-27T00:00:00.000Z");
    const trial = evaluate(null, { firstSeenAt: start, lastSeenAt: now });
    expect(trial.status).toBe("missing");
    expect(trial.writable).toBe(true);
    expect(trial.trialDaysLeft).toBe(25);
  });

  it("trial expires after 30 days", () => {
    const start = new Date("2026-04-01T00:00:00.000Z");
    const status = evaluate(null, { firstSeenAt: start, lastSeenAt: now });
    expect(status.status).toBe("trial_expired");
    expect(status.writable).toBe(false);
  });

  it("version mismatch", () => {
    const payload = { ...basePayload, version_range: "1.x" };
    expect(evaluate(payload).status).toBe("version_mismatch");
  });

  it("version range helper", () => {
    expect(versionMatches("0.x", "0.1.0")).toBe(true);
    expect(versionMatches("1.x", "0.1.0")).toBe(false);
    expect(versionMatches("*", "9.9.9")).toBe(true);
  });
});
