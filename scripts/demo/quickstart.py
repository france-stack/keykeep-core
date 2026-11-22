#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""KeyKeep 端到端 Demo：登录 → 接上游 → 建员工 → 签 Key → 真实转发 → 查账本 → 撤销 → 401。

这个脚本就是 README 里那条「五分钟跑通」的自动化版本，只用标准库，
对任何一套已经 `docker compose up -d` 起来的栈都能跑（也可以在桌面版上跑，
控制面 8443 / 数据面 8080 是同一个口径）。

它和 scripts/e2e/api_suite.py 的分工：

- api_suite.py 是验收用例集（104 条，覆盖拒绝路径、预算、限流、审计链、导出……），
  跑它需要 E2E 专用的 compose 叠加文件与固定账号；
- 本脚本是**给人看的最小证明**：一条链路从头走到尾，每一步打印人话，
  失败时直接指出是哪一步断了。它不写 evidence/，也不改任何验收状态。

两种上游：

- `--mock`：栈内已经有一个假上游（compose 叠加文件中叫 mock，容器内
  http://mock:19011；或宿主 127.0.0.1:19012）。不需要任何真实凭据。
- 真实供应商：给 `--base-url` 与 `--upstream-key`（或环境变量
  `<VENDOR>_API_KEY`，与 scripts/upstream/probe_and_accept.py 同一套命名）。
  注意控制面的 SSRF 防护默认拒绝私网地址，接内网中转时需要部署侧显式设
  KEYKEEP_ALLOW_PRIVATE_UPSTREAMS=true。

用法：

  # 对着 compose 栈跑（mock 上游在栈内）
  py scripts/demo/quickstart.py --mock --upstream-url http://mock:19011

  # 接真实 OpenAI
  OPENAI_API_KEY=sk-... py scripts/demo/quickstart.py \
      --provider openai --base-url https://api.openai.com/v1 \
      --upstream-key-env OPENAI_API_KEY --model gpt-4o-mini --alias demo-gpt

退码：0 全程通过；1 有步骤失败（会打印失败点与响应体摘录）。
"""

from __future__ import annotations

import argparse
import json
import os
import secrets
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from http.cookiejar import CookieJar

DEFAULT_CONTROL = os.environ.get("KEYKEEP_CONTROL", "http://127.0.0.1:8443")
DEFAULT_GATEWAY = os.environ.get("KEYKEEP_GATEWAY", "http://127.0.0.1:8080")

# 演示账号前缀：每次跑都新建，避免和已有数据打架，也方便重复跑。
RUN = secrets.token_hex(3)


class Resp:
    def __init__(self, status: int, headers: dict, body: bytes):
        self.status = status
        self.headers = headers
        self.body = body

    @property
    def text(self) -> str:
        return self.body.decode("utf-8", "replace")

    def json(self) -> dict:
        try:
            data = json.loads(self.body.decode("utf-8"))
            return data if isinstance(data, dict) else {}
        except (UnicodeDecodeError, json.JSONDecodeError):
            return {}

    def brief(self, limit: int = 300) -> str:
        return " ".join(self.text.split())[:limit]


class Client:
    def __init__(self, base: str):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

    def call(
        self,
        method: str,
        path: str,
        payload: dict | None = None,
        headers: dict[str, str] | None = None,
        timeout: float = 60,
    ) -> Resp:
        body = None
        hdrs = {"Accept": "application/json"}
        if payload is not None:
            body = json.dumps(payload).encode("utf-8")
            hdrs["Content-Type"] = "application/json"
        if headers:
            hdrs.update(headers)
        req = urllib.request.Request(self.base + path, data=body, headers=hdrs, method=method)
        try:
            with self.opener.open(req, timeout=timeout) as resp:
                return Resp(resp.status, dict(resp.headers), resp.read())
        except urllib.error.HTTPError as err:
            return Resp(err.code, dict(err.headers or {}), err.read())
        except Exception as err:  # noqa: BLE001 - 网络错误本身就是结果
            return Resp(0, {}, str(err).encode("utf-8"))


STEP = 0
FAILED: list[str] = []


def step(title: str) -> None:
    global STEP
    STEP += 1
    print(f"\n[{STEP}] {title}")


def ok(detail: str) -> None:
    print(f"    OK   {detail}")


def fail(detail: str) -> None:
    FAILED.append(detail)
    print(f"    FAIL {detail}")


def expect(resp: Resp, status: int, what: str, extra=None) -> bool:
    if resp.status != status:
        fail(f"{what}: 期望 HTTP {status}，实际 {resp.status} — {resp.brief()}")
        return False
    if extra is not None and not extra(resp):
        fail(f"{what}: 状态码对但内容不符 — {resp.brief()}")
        return False
    ok(f"{what}（HTTP {status}）")
    return True


def main() -> int:
    parser = argparse.ArgumentParser(description="KeyKeep 端到端 Demo")
    parser.add_argument("--control", default=DEFAULT_CONTROL, help="控制面地址（默认 8443）")
    parser.add_argument("--gateway", default=DEFAULT_GATEWAY, help="数据面地址（默认 8080）")
    parser.add_argument("--admin", default=os.environ.get("KEYKEEP_ADMIN", "admin"))
    parser.add_argument(
        "--admin-password",
        default=os.environ.get("KEYKEEP_ADMIN_PASSWORD", ""),
        help="管理员口令；不传则读 KEYKEEP_ADMIN_PASSWORD",
    )
    parser.add_argument("--mock", action="store_true", help="上游是栈内 mock（跳过凭据校验）")
    parser.add_argument("--provider", default="openai", help="供应商协议：openai / anthropic")
    parser.add_argument("--base-url", default="", help="上游 Base URL")
    parser.add_argument("--upstream-key", default="", help="上游真实 Key（谨慎：会进 shell 历史）")
    parser.add_argument("--upstream-key-env", default="", help="从该环境变量读上游 Key")
    parser.add_argument("--model", default="gpt-4o-mini", help="上游真实模型 ID")
    parser.add_argument("--alias", default=f"demo-{RUN}", help="逻辑模型别名（员工侧看到的名字）")
    parser.add_argument(
        "--price-prompt",
        type=int,
        default=0,
        help="输入价（分 / 100 万 token，0 = 不发布价格，账本会标 unpriced）",
    )
    parser.add_argument(
        "--price-completion",
        type=int,
        default=0,
        help="输出价（分 / 100 万 token）",
    )
    args = parser.parse_args()

    if not args.admin_password:
        print("需要管理员口令：--admin-password 或 KEYKEEP_ADMIN_PASSWORD", file=sys.stderr)
        return 2

    upstream_key = args.upstream_key or (
        os.environ.get(args.upstream_key_env, "") if args.upstream_key_env else ""
    )
    if not args.mock and not (args.base_url and upstream_key):
        print(
            "接真实上游需要 --base-url 与 --upstream-key/--upstream-key-env；"
            "只想验证链路请加 --mock 并给 --base-url 指向 mock。",
            file=sys.stderr,
        )
        return 2
    base_url = args.base_url or "http://mock:19011"
    provider_type = "openai" if args.provider == "openai" else "anthropic"

    print(f"控制面 {args.control} ｜ 数据面 {args.gateway}")
    print(f"上游 {provider_type} {base_url} ｜ 模型 {args.model} ｜ 别名 {args.alias}")

    admin = Client(args.control)

    step("控制面健康检查（数据库 / 迁移 / 审计链）")
    health = Client(args.control).call("GET", "/api/healthz")
    if not expect(health, 200, "GET /api/healthz"):
        print("\n    控制面没起来。先 `docker compose up -d` 并等 migrate 服务退出 0。")
        return 1

    step("管理员登录")
    if not expect(
        admin.call(
            "POST", "/api/auth/login", {"account": args.admin, "password": args.admin_password}
        ),
        200,
        "POST /api/auth/login",
    ):
        print(
            "\n    口令不对？compose 首装口令来自 .env 的 KEYKEEP_SEED_ADMIN_PASSWORD；"
            "没填则只在 migrate 容器日志里打印过一次。"
        )
        return 1

    step("接入上游供应商（密钥在控制面加密入库，不出现在日志里）")
    created = admin.call(
        "POST",
        "/api/admin/providers",
        {
            "name": f"demo-upstream-{RUN}",
            "providerType": provider_type,
            "baseUrl": base_url,
            "apiKey": upstream_key or f"sk-demo-{RUN}",
            "models": [{"alias": args.alias, "modelId": args.model}],
        },
    )
    if not expect(created, 201, "POST /api/admin/providers"):
        return 1
    provider_id = created.json().get("provider", {}).get("id")
    print(f"         provider id = {provider_id}")

    step("连通性测试（控制面替你去打一次上游）")
    test = admin.call("POST", f"/api/admin/providers/{provider_id}/test")
    if test.status == 200 and test.json().get("ok") is True:
        latency = test.json().get("latency_ms") or test.json().get("latencyMs")
        ok(f"上游可达，耗时 {latency if latency is not None else '?'} ms")
    else:
        # mock 上游不一定实现全部探测路径；真实上游不会走到这里。
        if args.mock:
            print(f"     WARN mock 上游的连通性探测未通过（{test.brief(120)}），继续验证转发链路")
        else:
            fail(f"上游不可达：{test.brief()}")

    if args.price_prompt or args.price_completion:
        step("发布模型价格（不发布也能转发，但账本会是 unpriced、费用恒为 0）")
        price = admin.call(
            "POST",
            "/api/admin/prices",
            {
                "provider": provider_type,
                "modelId": args.model,
                "version": f"demo-{RUN}",
                "promptPerM": args.price_prompt,
                "completionPerM": args.price_completion,
            },
        )
        expect(price, 201, "POST /api/admin/prices")
    else:
        print(
            "     SKIP 未给 --price-prompt/--price-completion："
            "这次调用在账本里会是 unpriced（费用恒为 0）"
        )

    step("建一个员工账号")
    account = f"demo_emp_{RUN}"
    password = secrets.token_urlsafe(12)
    emp = admin.call(
        "POST",
        "/api/admin/users",
        {
            "account": account,
            "displayName": f"Demo {RUN}",
            "role": "employee",
            "password": password,
        },
    )
    if not expect(emp, 201, "POST /api/admin/users"):
        return 1
    emp_id = emp.json().get("user", {}).get("id")

    step("给这名员工签发一把虚拟 Key（明文只返回这一次）")
    issued = admin.call(
        "POST", "/api/admin/keys", {"empId": emp_id, "count": 1, "allowedModels": [args.alias]}
    )
    if not expect(issued, 201, "POST /api/admin/keys"):
        return 1
    key = issued.json()["keys"][0]
    plaintext = key.get("plaintext") or key.get("key")
    print(f"         key id = {key.get('id')}  明文前缀 = {plaintext[:6]}…")

    step("员工用这把 Key 调一次真实转发")
    path = "/v1/messages" if provider_type == "anthropic" else "/v1/chat/completions"
    headers = {"Authorization": f"Bearer {plaintext}", "Content-Type": "application/json"}
    if provider_type == "anthropic":
        headers["anthropic-version"] = "2023-06-01"
        body = {
            "model": args.alias,
            "max_tokens": 64,
            "messages": [{"role": "user", "content": "Say hello in one short sentence."}],
        }
    else:
        body = {
            "model": args.alias,
            "max_tokens": 64,
            "messages": [{"role": "user", "content": "Say hello in one short sentence."}],
        }
    # 供应商 watcher 每 ~2s 重载一次配置，首调用可能还没看到新别名。
    call = None
    deadline = time.time() + 30
    while time.time() < deadline:
        call = Client(args.gateway).call("POST", path, body, headers)
        if call.status == 200:
            break
        time.sleep(1)
    if call is None or call.status != 200:
        fail(f"转发失败：{call.brief() if call else 'no response'}")
        return 1
    ok(f"转发成功：{call.brief(160)}")

    step("账本里能看到这次调用（价格 + token 数）")
    usage = admin.call("GET", f"/api/admin/usage/requests?empId={emp_id}&pageSize=5")
    if expect(usage, 200, "GET /api/admin/usage/requests"):
        rows = usage.json().get("requests") or usage.json().get("rows") or []
        if rows:
            row = rows[0]
            ok(
                "最新一条："
                f"model={row.get('modelReq')}→{row.get('modelServed')} "
                f"in={row.get('inputTokens')} out={row.get('outputTokens')} "
                f"cost={row.get('costCents')} 分 status={row.get('status')}"
            )
            if row.get("unpriced"):
                print("         WARN 这次调用没有命中价格表（unpriced），所以 cost=0")
        else:
            fail("账本为空：转发成功但没有入账（这是真实缺陷，不是演示问题）")

    step("撤销这把 Key，同一个请求立刻 401")
    revoked = admin.call("POST", f"/api/admin/keys/{key['id']}/revoke")
    expect(revoked, 200, "POST /api/admin/keys/{id}/revoke")
    after = Client(args.gateway).call("POST", path, body, headers)
    expect(after, 401, "撤销后再调用")

    print()
    if FAILED:
        print(f"结果：{len(FAILED)} 步失败")
        for item in FAILED:
            print(f"  - {item}")
        return 1
    print("结果：全链路通过（接入 → 签发 → 转发 → 记账 → 撤销）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
