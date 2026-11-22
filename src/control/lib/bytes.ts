/** Prisma 6 的 Bytes 字段需要 Uint8Array<ArrayBuffer>；Buffer 是 ArrayBufferLike。 */
export function toBytes(value: Buffer): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(value.length);
  out.set(value);
  return out;
}
