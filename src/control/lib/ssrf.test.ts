import { afterEach, describe, expect, it } from "vitest";

import { UpstreamUrlError, assertSafeUpstreamUrl } from "./ssrf";

afterEach(() => {
  delete process.env.KEYKEEP_ALLOW_PRIVATE_UPSTREAMS;
});

describe("assertSafeUpstreamUrl", () => {
  it("always allows loopback (desktop demo upstream)", async () => {
    await expect(assertSafeUpstreamUrl("http://127.0.0.1:19011/v1")).resolves.toBeUndefined();
    await expect(assertSafeUpstreamUrl("http://[::1]:19011/v1")).resolves.toBeUndefined();
  });

  it("always blocks the cloud metadata link-local range", async () => {
    await expect(assertSafeUpstreamUrl("http://169.254.169.254/latest/meta-data")).rejects.toMatchObject({
      name: "UpstreamUrlError",
      message: "blocked_link_local",
    });
    // IPv4 映射形态绕不过去：new URL() 会把 hostname 规范化成十六进制形态。
    await expect(assertSafeUpstreamUrl("http://[::ffff:169.254.169.254]/x")).rejects.toMatchObject({
      message: "blocked_link_local",
    });
    await expect(assertSafeUpstreamUrl("http://[::ffff:a9fe:a9fe]/x")).rejects.toMatchObject({
      message: "blocked_link_local",
    });
  });

  it("blocks IPv4 private ranges by default", async () => {
    for (const url of [
      "http://10.0.0.5:8000",
      "http://172.18.0.2:19011",
      "http://192.168.1.10:8000",
      // new URL() 十六进制形态的私网映射同样默认禁止。
      "http://[::ffff:a00:5]/x",
      "http://[::ffff:c0a8:101]/x",
    ]) {
      await expect(assertSafeUpstreamUrl(url)).rejects.toMatchObject({
        name: "UpstreamUrlError",
        message: "blocked_private",
      });
    }
  });

  it("lets KEYKEEP_ALLOW_PRIVATE_UPSTREAMS=true open IPv4 private ranges (E2E mock)", async () => {
    process.env.KEYKEEP_ALLOW_PRIVATE_UPSTREAMS = "true";
    await expect(assertSafeUpstreamUrl("http://172.18.0.2:19011/v1")).resolves.toBeUndefined();
    await expect(assertSafeUpstreamUrl("http://10.1.2.3:8000")).resolves.toBeUndefined();
    await expect(assertSafeUpstreamUrl("http://192.168.0.1:8000")).resolves.toBeUndefined();
    // 逃生门放开的只有私网：云元数据链路本地段仍然恒禁。
    await expect(assertSafeUpstreamUrl("http://169.254.169.254/latest/meta-data")).rejects.toMatchObject({
      message: "blocked_link_local",
    });
  });

  it("rejects non-http schemes and unparseable URLs", async () => {
    await expect(assertSafeUpstreamUrl("ftp://example.com")).rejects.toMatchObject({
      message: "scheme_not_allowed",
    });
    await expect(assertSafeUpstreamUrl("not a url")).rejects.toMatchObject({
      message: "invalid_url",
    });
  });

  it("raises UpstreamUrlError so routes can map it to upstream_not_allowed", async () => {
    await expect(assertSafeUpstreamUrl("http://0.0.0.0:8000")).rejects.toBeInstanceOf(UpstreamUrlError);
  });
});
