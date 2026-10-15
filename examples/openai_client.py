#!/usr/bin/env python3
"""Minimal OpenAI compatible client for the OpenCode Zen free models.

    pip install openai
    python examples/openai_client.py                       # 直连 OpenCode Zen
    BASE_URL=http://127.0.0.1:8320/v1 python examples/openai_client.py   # 走本仓库 sidecar
"""

import os
import sys

from openai import OpenAI

BASE_URL = os.environ.get("BASE_URL", "https://opencode.ai/zen/v1")
API_KEY = os.environ.get("OPENAI_API_KEY", "public")
MODEL = os.environ.get("MODEL", "grok-code")

client = OpenAI(base_url=BASE_URL, api_key=API_KEY)

print(f"# base_url={BASE_URL} model={MODEL}", file=sys.stderr)

models = client.models.list()
print(f"# {len(models.data)} models, e.g. {', '.join(m.id for m in models.data[:5])}", file=sys.stderr)

stream = client.chat.completions.create(
    model=MODEL,
    messages=[{"role": "user", "content": "用一句话解释什么是 OpenAI 兼容 API。"}],
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
print()
