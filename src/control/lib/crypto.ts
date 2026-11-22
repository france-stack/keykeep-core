import {
  createCipheriv,
  createDecipheriv,
  createHash,
  randomBytes,
  type CipherGCM,
  type DecipherGCM,
} from "node:crypto";

export const KEK_KID = "v1";
const KEK_BYTES = 32;
const NONCE_BYTES = 12;
const TAG_BYTES = 16;

export type EncryptedSecret = {
  ct: Buffer;
  nonce: Buffer;
  kid: string;
  keyFp6: string;
};

/** 读取 base64 编码的 32 字节 KEK；生产环境缺失即失败。 */
export function loadKek(raw = process.env.KEYKEEP_KEK): Buffer {
  if (!raw) {
    throw new Error("KEYKEEP_KEK is required (base64 of 32 bytes)");
  }
  const kek = Buffer.from(raw, "base64");
  if (kek.length !== KEK_BYTES) {
    throw new Error("KEYKEEP_KEK must decode to exactly 32 bytes");
  }
  return kek;
}

export function fingerprint(secret: string): string {
  return createHash("sha256").update(secret, "utf8").digest("hex").slice(-6);
}

/** AES-256-GCM；ct = ciphertext || authTag。 */
export function encryptSecret(plaintext: string, kek: Buffer): EncryptedSecret {
  if (kek.length !== KEK_BYTES) throw new Error("KEK must be 32 bytes");
  const nonce = randomBytes(NONCE_BYTES);
  const cipher = createCipheriv("aes-256-gcm", kek, nonce) as CipherGCM;
  const body = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
  return {
    ct: Buffer.concat([body, cipher.getAuthTag()]),
    nonce,
    kid: KEK_KID,
    keyFp6: fingerprint(plaintext),
  };
}

export function decryptSecret(ct: Buffer, nonce: Buffer, kek: Buffer): string {
  if (ct.length < TAG_BYTES) throw new Error("ciphertext too short");
  const decipher = createDecipheriv("aes-256-gcm", kek, nonce) as DecipherGCM;
  decipher.setAuthTag(ct.subarray(ct.length - TAG_BYTES));
  return Buffer.concat([
    decipher.update(ct.subarray(0, ct.length - TAG_BYTES)),
    decipher.final(),
  ]).toString("utf8");
}
