import { hash, verify } from "@node-rs/argon2";

export const PASSWORD_MIN_LENGTH = 12;
export const PASSWORD_MAX_LENGTH = 128;

const OPTIONS = {
  // 2 = Argon2id（@node-rs/argon2 的 Algorithm 是 ambient const enum，
  // isolatedModules 下不能直接引用，故用数值并在测试中校验 $argon2id$ 前缀）。
  algorithm: 2,
  memoryCost: 19456,
  timeCost: 2,
  parallelism: 1,
};

export function hashPassword(password: string): Promise<string> {
  return hash(password, OPTIONS);
}

/**
 * 哑哈希：账号不存在时也对它跑一次 Argon2 verify，抹平"账号不存在"与
 * "口令错误"的响应时间差，否则远程可以靠计时枚举有效账号名。
 * 用随机口令生成一次，永远验证失败。
 */
let dummyHashPromise: Promise<string> | null = null;
function dummyHash(): Promise<string> {
  dummyHashPromise ??= hash(randomSecret(), OPTIONS);
  return dummyHashPromise;
}

function randomSecret(): string {
  const bytes = new Uint8Array(24);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export async function verifyPassword(hashValue: string, password: string): Promise<boolean> {
  try {
    return await verify(hashValue, password);
  } catch {
    return false;
  }
}

/** 对不存在的账号执行的等耗时校验：结果恒为 false，但耗时与真实校验一致。 */
export async function verifyDummyPassword(password: string): Promise<boolean> {
  return verifyPassword(await dummyHash(), password);
}

export function validatePassword(password: string): string | null {
  if (password.length < PASSWORD_MIN_LENGTH) return "password_too_short";
  if (password.length > PASSWORD_MAX_LENGTH) return "password_too_long";
  return null;
}
