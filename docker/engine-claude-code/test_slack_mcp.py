"""Tests for the Slack MCP bot-mention filtering logic.

These tests verify that get_thread_replies_after only returns replies that
explicitly @-mention the bot, and that unrelated thread chatter is ignored.
"""

import time
from unittest.mock import MagicMock, patch

import httpx
import pytest

import slack_mcp


@pytest.fixture(autouse=True)
def _reset_globals():
    """Reset module-level globals between tests."""
    original_token = slack_mcp.SLACK_BOT_TOKEN
    original_channel = slack_mcp.SLACK_CHANNEL_ID
    original_user_id = slack_mcp.SLACK_BOT_USER_ID

    slack_mcp.SLACK_BOT_TOKEN = "xoxb-test-token"
    slack_mcp.SLACK_CHANNEL_ID = "C123TEST"
    slack_mcp.SLACK_BOT_USER_ID = "U0BOT123"

    yield

    slack_mcp.SLACK_BOT_TOKEN = original_token
    slack_mcp.SLACK_CHANNEL_ID = original_channel
    slack_mcp.SLACK_BOT_USER_ID = original_user_id


def _make_replies_response(messages: list[dict]) -> MagicMock:
    """Build a mock httpx response for conversations.replies."""
    resp = MagicMock()
    resp.json.return_value = {"ok": True, "messages": messages}
    return resp


class TestGetThreadRepliesAfter:
    """Tests for get_thread_replies_after bot-mention filtering."""

    @patch("slack_mcp.time.sleep", return_value=None)
    @patch("slack_mcp.httpx.Client")
    def test_message_with_bot_mention_is_returned(self, mock_client_cls, _sleep):
        """Given a non-bot message that @-mentions the bot, it should be returned."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.get.return_value = _make_replies_response([
            {"ts": "1000.0", "text": "root message"},
            {"ts": "1001.0", "text": "<@U0BOT123> yes, go ahead"},
        ])

        result = slack_mcp.get_thread_replies_after("1000.0", "1000.0", timeout_seconds=1)
        assert result == "<@U0BOT123> yes, go ahead"

    @patch("slack_mcp.time.sleep", return_value=None)
    @patch("slack_mcp.httpx.Client")
    def test_message_without_bot_mention_is_ignored(self, mock_client_cls, _sleep):
        """Given a non-bot message that does NOT @-mention the bot, it should be skipped."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.get.return_value = _make_replies_response([
            {"ts": "1000.0", "text": "root message"},
            {"ts": "1001.0", "text": "some unrelated chatter in the thread"},
        ])

        result = slack_mcp.get_thread_replies_after("1000.0", "1000.0", timeout_seconds=1)
        assert result is None

    @patch("slack_mcp.time.sleep", return_value=None)
    @patch("slack_mcp.httpx.Client")
    def test_bot_message_with_mention_is_skipped(self, mock_client_cls, _sleep):
        """Given a bot message (has bot_id) even if it mentions the bot, it should be skipped."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.get.return_value = _make_replies_response([
            {"ts": "1000.0", "text": "root message"},
            {"ts": "1001.0", "text": "<@U0BOT123> echo", "bot_id": "B999"},
        ])

        result = slack_mcp.get_thread_replies_after("1000.0", "1000.0", timeout_seconds=1)
        assert result is None

    @patch("slack_mcp.time.sleep", return_value=None)
    @patch("slack_mcp.httpx.Client")
    def test_message_before_after_ts_is_skipped(self, mock_client_cls, _sleep):
        """Given a message with ts <= after_ts, it should be skipped even if it mentions the bot."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.get.return_value = _make_replies_response([
            {"ts": "1000.0", "text": "<@U0BOT123> old message"},
        ])

        result = slack_mcp.get_thread_replies_after("1000.0", "1000.0", timeout_seconds=1)
        assert result is None

    @patch("slack_mcp.time.sleep", return_value=None)
    @patch("slack_mcp.httpx.Client")
    def test_no_bot_user_id_accepts_any_non_bot_message(self, mock_client_cls, _sleep):
        """When bot user ID is unknown, fall back to accepting any non-bot reply."""
        slack_mcp.SLACK_BOT_USER_ID = ""

        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.get.return_value = _make_replies_response([
            {"ts": "1000.0", "text": "root message"},
            {"ts": "1001.0", "text": "reply without any mention"},
        ])

        result = slack_mcp.get_thread_replies_after("1000.0", "1000.0", timeout_seconds=1)
        assert result == "reply without any mention"


class TestResolveBotUserId:
    """Tests for resolve_bot_user_id."""

    @patch("slack_mcp.httpx.Client")
    def test_successful_resolution(self, mock_client_cls):
        """Given a valid bot token, auth.test should return the user_id."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        resp = MagicMock()
        resp.json.return_value = {"ok": True, "user_id": "URESOLVED"}
        mock_client.post.return_value = resp

        result = slack_mcp.resolve_bot_user_id()
        assert result == "URESOLVED"

    @patch("slack_mcp.httpx.Client")
    def test_failed_auth_returns_empty(self, mock_client_cls):
        """Given a failed auth.test response, return empty string."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        resp = MagicMock()
        resp.json.return_value = {"ok": False, "error": "invalid_auth"}
        mock_client.post.return_value = resp

        result = slack_mcp.resolve_bot_user_id()
        assert result == ""

    @patch("slack_mcp.httpx.Client")
    def test_network_error_returns_empty(self, mock_client_cls):
        """Given an httpx exception during auth.test, return empty string gracefully."""
        mock_client = MagicMock()
        mock_client_cls.return_value.__enter__ = MagicMock(return_value=mock_client)
        mock_client_cls.return_value.__exit__ = MagicMock(return_value=False)

        mock_client.post.side_effect = httpx.ConnectError("connection refused")

        result = slack_mcp.resolve_bot_user_id()
        assert result == ""

    def test_no_token_returns_empty(self):
        """Given no bot token configured, return empty string without calling the API."""
        slack_mcp.SLACK_BOT_TOKEN = ""
        result = slack_mcp.resolve_bot_user_id()
        assert result == ""


class TestAskHumanMentionHint:
    """Tests for the ask_human question text including bot mention hint."""

    @patch("slack_mcp.get_thread_replies_after", return_value="<@U0BOT123> yes")
    @patch("slack_mcp.send_slack_message")
    def test_question_includes_tag_hint_when_bot_id_known(self, mock_send, _mock_replies):
        """Given a known bot user ID, the question should tell users to tag the bot."""
        mock_send.return_value = {"ok": True, "ts": "1234.0"}

        slack_mcp.handle_ask_human("What colour?")

        posted_text = mock_send.call_args_list[0][0][0]
        assert "<@U0BOT123>" in posted_text
        assert "Tag" in posted_text

    @patch("slack_mcp.get_thread_replies_after", return_value="sure")
    @patch("slack_mcp.send_slack_message")
    def test_question_uses_generic_hint_when_bot_id_unknown(self, mock_send, _mock_replies):
        """Given no bot user ID, fall back to the generic reply hint."""
        slack_mcp.SLACK_BOT_USER_ID = ""
        mock_send.return_value = {"ok": True, "ts": "1234.0"}

        slack_mcp.handle_ask_human("What colour?")

        posted_text = mock_send.call_args_list[0][0][0]
        assert "Reply in this thread" in posted_text
