"""Unit tests for the Slack-forwarder Lambda's message formatting.

Run: python3 -m unittest discover -s stack/terraform/modules/observability/forwarder

format_slack_message and _alarm_from_record are pure (no AWS calls) and tested
directly. The handler's batch loop is tested by stubbing _post_to_slack, so the
per-record isolation (one Slack failure must not drop the rest) is covered without
real network or Secrets Manager I/O.
"""

import json
import sys
import types
import unittest

# boto3 ships in the Lambda runtime but is not a test dependency, and
# format_slack_message never touches it. Stub the module so importing the handler
# works on a bare interpreter (e.g. CI) without installing boto3.
if "boto3" not in sys.modules:
    boto3_stub = types.ModuleType("boto3")
    boto3_stub.client = lambda *_args, **_kwargs: None
    sys.modules["boto3"] = boto3_stub

import handler


class FormatSlackMessageTest(unittest.TestCase):
    def test_alarm_state_includes_name_state_and_reason(self):
        msg = handler.format_slack_message(
            {
                "AlarmName": "truth-in-stream-prod-alb-5xx",
                "NewStateValue": "ALARM",
                "NewStateReason": "Threshold Crossed: 1 datapoint [12.0] was greater than 5.0",
                "Region": "EU (Paris)",
            }
        )
        text = msg["text"]
        self.assertIn("truth-in-stream-prod-alb-5xx", text)
        self.assertIn("ALARM", text)
        self.assertIn("Threshold Crossed", text)
        self.assertIn("EU (Paris)", text)
        self.assertIn(":red_circle:", text)

    def test_ok_state_uses_green_emoji(self):
        msg = handler.format_slack_message(
            {"AlarmName": "x", "NewStateValue": "OK", "NewStateReason": "back to normal"}
        )
        self.assertIn(":large_green_circle:", msg["text"])
        self.assertIn("OK", msg["text"])

    def test_insufficient_data_state_uses_white_emoji(self):
        msg = handler.format_slack_message(
            {"AlarmName": "x", "NewStateValue": "INSUFFICIENT_DATA", "NewStateReason": ""}
        )
        self.assertIn(":white_circle:", msg["text"])

    def test_unknown_state_falls_back_to_warning_emoji(self):
        msg = handler.format_slack_message({"AlarmName": "x", "NewStateValue": "WAT"})
        self.assertIn(":warning:", msg["text"])

    def test_missing_fields_do_not_raise(self):
        msg = handler.format_slack_message({})
        self.assertIn("unknown alarm", msg["text"])
        self.assertIn("UNKNOWN", msg["text"])

    def test_empty_reason_and_region_are_omitted(self):
        msg = handler.format_slack_message({"AlarmName": "a", "NewStateValue": "ALARM"})
        # Header only: no trailing blank lines for absent reason/region.
        self.assertEqual(msg["text"].count("\n"), 0)


class AlarmFromRecordTest(unittest.TestCase):
    def test_parses_json_message(self):
        record = {"Sns": {"Message": '{"AlarmName": "a", "NewStateValue": "ALARM"}'}}
        self.assertEqual(handler._alarm_from_record(record)["AlarmName"], "a")

    def test_non_json_message_becomes_raw_notification(self):
        alarm = handler._alarm_from_record({"Sns": {"Message": "plain text"}})
        self.assertEqual(alarm["AlarmName"], "raw notification")
        self.assertIn("plain text", alarm["NewStateReason"])


class HandlerBatchTest(unittest.TestCase):
    def setUp(self):
        self._orig_post = handler._post_to_slack
        self.posted = []

    def tearDown(self):
        handler._post_to_slack = self._orig_post

    def _record(self, name):
        return {"Sns": {"Message": json.dumps({"AlarmName": name, "NewStateValue": "ALARM"})}}

    def test_forwards_every_record(self):
        handler._post_to_slack = lambda payload: self.posted.append(payload)
        result = handler.handler({"Records": [self._record("a"), self._record("b")]})
        self.assertEqual(result, {"forwarded": 2})
        self.assertEqual(len(self.posted), 2)

    def test_one_record_failure_does_not_drop_the_rest_and_raises(self):
        def flaky(payload):
            if "boom" in payload["text"]:
                raise RuntimeError("slack down")
            self.posted.append(payload)

        handler._post_to_slack = flaky
        with self.assertRaises(RuntimeError):
            handler.handler({"Records": [self._record("ok-1"), self._record("boom"), self._record("ok-2")]})
        # Both healthy records were still posted before the handler re-raised.
        self.assertEqual(len(self.posted), 2)


if __name__ == "__main__":
    unittest.main()
