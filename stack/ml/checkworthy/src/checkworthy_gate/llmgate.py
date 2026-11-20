"""Record the current LLM gate's verdicts over the golden set.

The recorded labels feed the committed gate-agreement eval fixture, so CI can
enforce "local decisions outside the band agree with the LLM gate" without a
network call. The prompt and forced-tool contract mirror the backend's
internal/checkworthy package exactly (French prompt, record_check_worthiness
tool, unsure means not check-worthy); this module exists only to snapshot
those verdicts once per training run.
"""

from __future__ import annotations

import json
import time
import urllib.request

DEEPSEEK_URL = "https://api.deepseek.com/chat/completions"
DEEPSEEK_MODEL = "deepseek-chat"

SYSTEM_PROMPT_FR = (
    "Tu evalues si un seul enonce parle est une affirmation factuelle publique verifiable. "
    "Un enonce est verifiable uniquement lorsqu'il avance une affirmation publique et verifiable sur le monde - "
    "un fait, une statistique, un evenement, ou une declaration attribuable qui pourrait etre confirmee ou refutee a partir de preuves. "
    "Il n'est PAS verifiable lorsqu'il s'agit d'une conversation anodine, d'une declaration personnelle ou banale (\"j'ai pris un cafe ce matin\", "
    "\"mon vol etait en retard\"), d'une opinion, d'une question, d'une salutation, d'une formule prudente, ou d'un fragment de phrase. "
    "En cas de doute, enregistre-le comme non verifiable. Enregistre ton verdict avec l'outil record_check_worthiness."
)

TOOL = {
    "type": "function",
    "function": {
        "name": "record_check_worthiness",
        "description": "Record whether the statement is a check-worthy public factual claim.",
        "parameters": {
            "type": "object",
            "properties": {
                "check_worthy": {"type": "boolean"},
                "reason": {"type": "string"},
            },
            "required": ["check_worthy", "reason"],
        },
    },
}


def check_worthy(api_key: str, text: str, retries: int = 3) -> bool:
    """One forced-tool judgment for one statement."""
    payload = {
        "model": DEEPSEEK_MODEL,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT_FR},
            {"role": "user", "content": text},
        ],
        "tools": [TOOL],
        "tool_choice": {"type": "function", "function": {"name": "record_check_worthiness"}},
        "temperature": 0,
    }
    request = urllib.request.Request(
        DEEPSEEK_URL,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"},
    )
    last_error: Exception | None = None
    for attempt in range(retries):
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                body = json.load(response)
            call = body["choices"][0]["message"]["tool_calls"][0]
            arguments = json.loads(call["function"]["arguments"])
            return bool(arguments["check_worthy"])
        except Exception as exc:  # noqa: BLE001 - retried, then surfaced
            last_error = exc
            time.sleep(2**attempt)
    raise RuntimeError(f"llm gate call failed after {retries} attempts: {last_error}")
