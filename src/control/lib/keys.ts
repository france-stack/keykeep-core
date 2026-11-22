import { createHash, randomBytes } from "node:crypto";

export type IssuedKey = {
  plaintext: string;
  keyHash: Buffer;
  prefix4: string;
  fp6: string;
};

/** 生成虚拟 Key：明文只返回一次，库里只存 SHA-256。 */
export function issueKey(): IssuedKey {
  const secret = randomBytes(32).toString("base64url");
  const plaintext = `kk_${secret}`;
  const digest = createHash("sha256").update(plaintext, "utf8").digest();
  const hex = digest.toString("hex");
  return {
    plaintext,
    keyHash: digest,
    prefix4: plaintext.slice(0, 4),
    fp6: hex.slice(-6),
  };
}

export function hashKey(plaintext: string): Buffer {
  return createHash("sha256").update(plaintext, "utf8").digest();
}

export function isKeyFormat(value: string): boolean {
  return /^kk_[A-Za-z0-9_-]{43}$/.test(value);
}

export function keyFingerprint(prefix4: string, fp6: string): string {
  return `${prefix4}…${fp6}`;
}
