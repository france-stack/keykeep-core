/**
 * 脱敏规则的形状闸（纯字符扫描，无动态正则构造、无正则字面量）。
 *
 * 规则的执行方是 Go 网关的 regexp（RE2 语义：线性时间，免疫回溯型 ReDoS）。
 * 控制面在入库前拒绝经典回溯炸弹形状——嵌套量词、互为前缀的分支；
 * 这类模式即使对 RE2 也有意义：会撑爆其程序规模上限，导致规则被网关静默跳过。
 * 真正的可编译性编译校验由网关加载时完成（非法规则被跳过并记日志）。
 */

/** 模式长度上限：足够覆盖合法脱敏规则，压缩病态模式的构造空间。 */
export const MAX_REGEX_SHAPE_LENGTH = 256;

function isDigit(c: string): boolean {
  return c >= "0" && c <= "9";
}

/** 判断从 at 开始是否是 {n} / {n,} / {n,m} 形式的量词（调用方已确认首字符是 {）。 */
function isBraceQuantifier(text: string, at: number): boolean {
  if (text[at] !== "{") return false;
  let i = at + 1;
  let digits = 0;
  while (i < text.length && isDigit(text[i])) {
    i += 1;
    digits += 1;
  }
  if (digits === 0) return false;
  if (text[i] === "}") return true;
  if (text[i] !== ",") return false;
  i += 1;
  while (i < text.length && isDigit(text[i])) {
    i += 1;
  }
  return text[i] === "}";
}

/** 判断组体里是否含有量词（+ * 或 {n,m}）；字符类 [..] 与转义不算。 */
function groupBodyHasQuantifier(body: string): boolean {
  let k = 0;
  while (k < body.length) {
    const c = body[k];
    if (c === "\\") {
      k += 2;
      continue;
    }
    if (c === "[") {
      k += 1;
      while (k < body.length && body[k] !== "]") {
        if (body[k] === "\\") k += 1;
        k += 1;
      }
      k += 1;
      continue;
    }
    if (c === "+" || c === "*") return true;
    if (c === "{" && isBraceQuantifier(body, k)) return true;
    k += 1;
  }
  return false;
}

function hasPrefixOverlappingAlternatives(body: string): boolean {
  if (!body.includes("|")) return false;
  const alternatives = body.split("|");
  for (let a = 0; a < alternatives.length; a += 1) {
    for (let b = 0; b < alternatives.length; b += 1) {
      if (a !== b && alternatives[a].length > 0 && alternatives[b].startsWith(alternatives[a])) {
        return true;
      }
    }
  }
  return false;
}

/**
 * 返回 null 表示形状安全；否则返回命中的危险形状名。
 * 扫描所有分组：配对忽略字符类与转义；组后紧跟量词时按两种危险形状判定。
 */
export function unsafeRegexShape(pattern: string): string | null {
  if (pattern.length > MAX_REGEX_SHAPE_LENGTH) return "too_long";
  let i = 0;
  while (i < pattern.length) {
    const ch = pattern[i];
    if (ch === "\\") {
      i += 2;
      continue;
    }
    if (ch !== "(") {
      i += 1;
      continue;
    }
    let depth = 1;
    let j = i + 1;
    let inClass = false;
    while (j < pattern.length && depth > 0) {
      const c = pattern[j];
      if (c === "\\") {
        j += 2;
        continue;
      }
      if (inClass) {
        if (c === "]") inClass = false;
      } else if (c === "[") {
        inClass = true;
      } else if (c === "(") {
        depth += 1;
      } else if (c === ")") {
        depth -= 1;
        if (depth === 0) break;
      }
      j += 1;
    }
    if (depth !== 0) return null;
    const body = pattern.slice(i + 1, j);
    const after = pattern.slice(j + 1);
    const quantified = after.startsWith("+") || after.startsWith("*") || isBraceQuantifier(after, 0);
    if (quantified) {
      if (groupBodyHasQuantifier(body)) return "nested_quantifier";
      if (hasPrefixOverlappingAlternatives(body)) return "unsafe_alternation";
    }
    i = j + 1;
  }
  return null;
}
