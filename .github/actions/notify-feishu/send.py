#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把 Rollin 部署事件渲染成飞书互动卡片，推送到群机器人。

仅用于 GitHub Actions，只依赖 Python 3 标准库（runner 自带 python3，无需 pip 安装）。

两条硬约束：
  1. 通知失败绝不把部署判为失败 —— 所有异常降级为 ::warning:: 且以 0 退出。
  2. webhook 地址本身就是密钥 —— 任何日志、错误信息里都不出现它。

输入全部经环境变量传入（见同目录 action.yml），不接收命令行参数。
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

CN_TZ = timezone(timedelta(hours=8))

# 阶段 -> (卡片主题色, 图标, 动作词)
_STATUS = {
    "started": ("blue", "🚀", "开始部署"),
    "success": ("green", "✅", "部署成功"),
    "failure": ("red", "❌", "部署失败"),
}

_RESULT_LABEL = {
    "success": "成功",
    "failure": "失败",
    "cancelled": "已取消",
    "skipped": "已跳过",
}


def warn(message: str) -> None:
    print(f"::warning::{message}")


# ---------------------------------------------------------------------------
# 卡片元素
# ---------------------------------------------------------------------------
def field(label: str, value: str, short: bool = True) -> dict:
    return {"is_short": short, "text": {"tag": "lark_md", "content": f"**{label}**\n{value}"}}


def div(content: str) -> dict:
    return {"tag": "div", "text": {"tag": "lark_md", "content": content}}


def clip(text: str, limit: int) -> str:
    text = (text or "").strip()
    return text if len(text) <= limit else text[: limit - 1] + "…"


# ---------------------------------------------------------------------------
# 提交信息：优先 GitHub 事件，手动触发时退回工作区 git
# ---------------------------------------------------------------------------
def git(*args: str) -> str:
    try:
        proc = subprocess.run(
            ["git", *args], capture_output=True, text=True, timeout=15, check=True
        )
    except Exception:
        return ""
    return proc.stdout.strip()


def load_commits() -> list[dict]:
    raw = (os.environ.get("COMMITS_JSON") or "").strip()
    if raw and raw != "null":
        try:
            data = json.loads(raw)
        except ValueError:
            data = None
        if isinstance(data, list) and data:
            commits = []
            for item in data:
                if not isinstance(item, dict):
                    continue
                author = item.get("author") or {}
                commits.append(
                    {
                        "id": str(item.get("id") or ""),
                        "message": str(item.get("message") or ""),
                        "author": str((author or {}).get("name") or ""),
                    }
                )
            if commits:
                return commits

    # workflow_dispatch 没有 commits 数组；部署目录可能也没 checkout，失败即返回空
    head = git("rev-parse", "HEAD")
    if not head:
        return []
    return [
        {
            "id": head,
            "message": git("log", "-1", "--pretty=format:%B"),
            "author": git("log", "-1", "--pretty=format:%an"),
        }
    ]


def summarize(message: str, limit: int = 240, max_lines: int = 3) -> str:
    lines = [ln.rstrip() for ln in (message or "").splitlines() if ln.strip()]
    if not lines:
        return "（无提交说明）"
    body = "\n".join(lines[:max_lines])
    if len(lines) > max_lines:
        body += f"\n…（另有 {len(lines) - max_lines} 行）"
    return clip(body, limit)


def failure_stage(build_result: str, deploy_result: str) -> str:
    bad = [
        f"{name}（{_RESULT_LABEL.get(result, result)}）"
        for name, result in (("构建", build_result), ("部署", deploy_result))
        if result and result != "success"
    ]
    return " → ".join(bad) or "未能识别到具体阶段"


def human_duration(seconds: float) -> str:
    total = max(0, int(seconds))
    if total < 60:
        return f"{total} 秒"
    minutes, sec = divmod(total, 60)
    if minutes < 60:
        return f"{minutes} 分 {sec} 秒"
    hours, minutes = divmod(minutes, 60)
    return f"{hours} 小时 {minutes} 分"


# ---------------------------------------------------------------------------
# 卡片
# ---------------------------------------------------------------------------
def build_card(commits: list[dict]) -> dict:
    status = (os.environ.get("STATUS") or "").strip()
    color, emoji, action = _STATUS.get(status, ("grey", "ℹ️", "部署事件"))
    service = (os.environ.get("SERVICE") or "服务").strip()

    short_sha = (os.environ.get("SHA") or "").strip()[:7] or "unknown"
    ref = (os.environ.get("REF_NAME") or "-").strip()
    actor = (os.environ.get("ACTOR") or "-").strip()
    event = (os.environ.get("EVENT_NAME") or "").strip()
    run_url = (os.environ.get("RUN_URL") or "").strip()

    if event == "push":
        trigger = actor
    elif event == "workflow_dispatch":
        trigger = f"{actor}（手动触发）"
    else:
        trigger = f"{actor}（{event or '未知事件'}）"

    elements: list[dict] = [
        {"tag": "div", "fields": [field("提交", short_sha), field("分支", ref)]},
        {
            "tag": "div",
            "fields": [
                field("触发", trigger),
                field("时间", datetime.now(CN_TZ).strftime("%Y-%m-%d %H:%M:%S")),
            ],
        },
        {"tag": "hr"},
    ]

    if commits:
        elements.append(div(f"**提交说明**\n{summarize(commits[0]['message'])}"))
    else:
        elements.append(div("**提交说明**\n（未能读取到提交信息）"))

    # 一次 push 含多个提交时，只报最后一个会漏掉信息量，这里补一份清单
    if len(commits) > 1:
        lines = []
        for item in commits[:5]:
            first_line = item["message"].strip().splitlines()
            subject = clip(first_line[0], 80) if first_line else "（无说明）"
            line = f"- {item['id'][:7] or '???????'} {subject}"
            if item["author"]:
                line += f"（{item['author']}）"
            lines.append(line)
        if len(commits) > 5:
            lines.append(f"- …（共 {len(commits)} 个提交，仅列出前 5 个）")
        elements.append({"tag": "hr"})
        elements.append(div(f"**本次推送 {len(commits)} 个提交**\n" + "\n".join(lines)))

    if status == "failure":
        stage = failure_stage(
            os.environ.get("BUILD_RESULT", ""), os.environ.get("DEPLOY_RESULT", "")
        )
        elements.append({"tag": "hr"})
        elements.append(div(f"**失败阶段**\n{stage}"))

    started_at = (os.environ.get("STARTED_AT") or "").strip()
    if status != "started" and started_at:
        try:
            duration = human_duration(time.time() - float(started_at))
        except ValueError:
            duration = ""
        if duration:
            elements.append(div(f"**用时**\n{duration}"))

    if run_url:
        elements.append(
            {
                "tag": "action",
                "actions": [
                    {
                        "tag": "button",
                        "text": {"tag": "plain_text", "content": "查看运行详情"},
                        "type": "danger" if status == "failure" else "primary",
                        "url": run_url,
                    }
                ],
            }
        )

    return {
        "config": {"wide_screen_mode": True},
        "header": {
            "template": color,
            "title": {"tag": "plain_text", "content": f"{emoji} Rollin {service}{action}"},
        },
        "elements": elements,
    }


# ---------------------------------------------------------------------------
# 签名与发送
# ---------------------------------------------------------------------------
def sign(timestamp: str, secret: str) -> str:
    """飞书自定义机器人签名：key 为 "<timestamp>\\n<secret>"，待签内容为空。"""
    key = f"{timestamp}\n{secret}".encode("utf-8")
    return base64.b64encode(hmac.new(key, b"", hashlib.sha256).digest()).decode("utf-8")


def post(url: str, payload: dict) -> tuple[int, str]:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": "application/json; charset=utf-8"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=15) as response:
        return response.status, response.read().decode("utf-8", "replace")


def main() -> int:
    url = (os.environ.get("WEBHOOK_URL") or "").strip()
    if not url:
        warn("未配置飞书机器人地址（Secrets.FEISHU_WEBHOOK），跳过部署通知")
        return 0
    if not url.startswith("https://"):
        warn("飞书机器人地址必须是 https:// 开头，跳过部署通知")
        return 0

    payload = {"msg_type": "interactive", "card": build_card(load_commits())}

    secret = (os.environ.get("WEBHOOK_SECRET") or "").strip()
    if secret:
        stamp = str(int(time.time()))
        payload["timestamp"] = stamp
        payload["sign"] = sign(stamp, secret)

    try:
        http_status, text = post(url, payload)
    except Exception as exc:  # 网络不可达、超时、非 2xx 等
        warn(f"飞书通知发送失败：{str(exc).replace(url, '<webhook>')}")
        return 0

    try:
        data = json.loads(text)
    except ValueError:
        data = {}

    code = data.get("code", data.get("StatusCode", 0))
    if code not in (0, "0"):
        warn(f"飞书通知被拒绝（HTTP {http_status}）：{clip(text, 200)}")
        return 0

    print(f"飞书通知已发送：{os.environ.get('SERVICE')} / {os.environ.get('STATUS')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
