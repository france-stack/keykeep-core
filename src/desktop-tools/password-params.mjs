// 桌面版脚本的口令哈希参数——桌面侧唯一的一份定义。
//
// 为什么单独成文件：argon2 的参数在 src/control/lib/password.ts 与
// seed-desktop.mjs 里各抄了一份，加入口令找回脚本就是第三份。手抄的安全参数
// 迟早漂移，而这类漂移不会立刻报错（哈希串自带参数，verify 按哈希里的参数算），
// 只会悄悄改变每次登录的算力开销。收敛成一份，并由
// tests/unit/test_password_params_contract.py 与 TS 侧对账。
//
// algorithm=2 是 Argon2id。@node-rs/argon2 的 Algorithm 是 ambient const enum，
// 这里直接写数值，与 lib/password.ts 的处理方式一致。
import { hash } from "@node-rs/argon2";

export const ARGON_OPTIONS = { algorithm: 2, memoryCost: 19456, timeCost: 2, parallelism: 1 };

// 与 lib/password.ts 的 PASSWORD_MIN_LENGTH / PASSWORD_MAX_LENGTH 一致：
// 口令策略由服务端把关，脚本不能自己放宽一条「只有脚本知道」的规则。
export const PASSWORD_MIN_LENGTH = 12;
export const PASSWORD_MAX_LENGTH = 128;

export function hashPassword(password) {
  return hash(password, ARGON_OPTIONS);
}

export function passwordPolicyError(password) {
  if (typeof password !== "string" || password.length < PASSWORD_MIN_LENGTH) {
    return `口令太短（至少 ${PASSWORD_MIN_LENGTH} 位）`;
  }
  if (password.length > PASSWORD_MAX_LENGTH) {
    return `口令太长（最多 ${PASSWORD_MAX_LENGTH} 位）`;
  }
  return null;
}
