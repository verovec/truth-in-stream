"""CloudWatch-alarm-to-Slack forwarder.

Subscribed to the alerts SNS topic. Each SNS record carries a CloudWatch alarm
state-change notification; this turns it into a Slack message and posts it to the
incoming webhook whose URL is read from Secrets Manager (never baked into the
function). The webhook URL is cached across warm invocations so a burst of alarms
does not re-read the secret every time.

The runtime has no third-party dependencies: it uses boto3 (bundled in the Lambda
runtime) for Secrets Manager and urllib for the webhook POST, so the function
deploys as a single source file with no build step.
"""

import json
import os
import urllib.request

import boto3

_WEBHOOK_SECRET_ARN = os.environ.get("SLACK_WEBHOOK_SECRET_ARN", "")

# Emoji per alarm state so an operator can triage from the notification list
# alone. ALARM is the only state that should normally page; OK closes the loop.
_STATE_EMOJI = {
    "ALARM": ":red_circle:",
    "OK": ":large_green_circle:",
    "INSUFFICIENT_DATA": ":white_circle:",
}

_secretsmanager = boto3.client("secretsmanager")
_cached_webhook_url = None


def format_slack_message(alarm: dict) -> dict:
    """Build the Slack webhook payload from a parsed CloudWatch alarm message.

    Pure: no I/O, so it is unit-tested directly. ``alarm`` is the JSON object
    CloudWatch publishes to SNS (already parsed from the SNS ``Message`` string).
    """
    name = alarm.get("AlarmName", "unknown alarm")
    new_state = alarm.get("NewStateValue", "UNKNOWN")
    reason = alarm.get("NewStateReason", "")
    region = alarm.get("Region", "")
    emoji = _STATE_EMOJI.get(new_state, ":warning:")

    header = f"{emoji} *{name}* is now *{new_state}*"
    lines = [header]
    if reason:
        lines.append(reason)
    if region:
        lines.append(f"_region: {region}_")

    return {"text": "\n".join(lines)}


def _webhook_url() -> str:
    global _cached_webhook_url
    if _cached_webhook_url is None:
        if not _WEBHOOK_SECRET_ARN:
            raise RuntimeError("SLACK_WEBHOOK_SECRET_ARN is not set")
        secret = _secretsmanager.get_secret_value(SecretId=_WEBHOOK_SECRET_ARN)
        _cached_webhook_url = secret["SecretString"].strip()
    return _cached_webhook_url


def _post_to_slack(payload: dict) -> None:
    request = urllib.request.Request(
        _webhook_url(),
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        response.read()


def _alarm_from_record(record: dict) -> dict:
    message = record.get("Sns", {}).get("Message", "")
    try:
        return json.loads(message)
    except (TypeError, ValueError):
        # A non-JSON SNS message (e.g. a manual publish) still gets forwarded as a
        # raw notification rather than dropped silently.
        return {"AlarmName": "raw notification", "NewStateValue": "ALARM", "NewStateReason": str(message)}


def handler(event, _context=None):
    """SNS entry point. Forwards every alarm record in the event to Slack.

    Each record is posted independently: one record's Slack failure must not drop
    the others in the same batch. If any record fails, the handler re-raises after
    attempting them all so SNS retries the invocation; already-delivered records in
    the batch may then be re-posted, which is acceptable (an alarm is better seen
    twice than missed). SNS->Lambda fan-out is one record per invocation in
    practice, so a retry normally re-sends a single record.
    """
    records = event.get("Records", [])
    forwarded = 0
    errors = []
    for record in records:
        try:
            _post_to_slack(format_slack_message(_alarm_from_record(record)))
            forwarded += 1
        except Exception as exc:  # noqa: BLE001 - isolate one record's failure
            errors.append(exc)
    if errors:
        raise RuntimeError(f"{len(errors)} of {len(records)} alarm notifications failed to forward") from errors[0]
    return {"forwarded": forwarded}
