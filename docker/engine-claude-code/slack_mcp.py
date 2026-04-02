#!/usr/bin/env python3
"""Simple stdio MCP server for Slack and GitLab/CodeRabbit integration.

This runs locally inside the Claude Code container and communicates
with Slack directly using the Bot token, and with GitLab for pipeline
and CodeRabbit comment monitoring.
"""

import json
import os
import re
import sys
import time
from typing import Any

import httpx

# Slack configuration from environment
SLACK_BOT_TOKEN = os.environ.get("SLACK_BOT_TOKEN", "")
SLACK_CHANNEL_ID = os.environ.get("SLACK_CHANNEL_ID", "")
# Thread timestamp injected by the controller when a top-level notification
# has already been sent.  When set, all agent messages are replies in that
# thread rather than separate top-level messages.
SLACK_THREAD_TS = os.environ.get("SLACK_THREAD_TS", "")

# Resolved at startup via auth.test — the Slack user ID of this bot so we can
# require an @-mention before treating a thread reply as directed at us.
SLACK_BOT_USER_ID: str = ""

# GitLab configuration from environment
GITLAB_TOKEN = os.environ.get("GITLAB_TOKEN", "")
GITLAB_API_BASE = "https://gitlab.com/api/v4"

# CodeRabbit bot username
CODERABBIT_BOT_NAME = "coderabbitai"


def log(msg: str) -> None:
    """Log to stderr (stdout is reserved for MCP protocol)."""
    print(f"[slack-mcp] {msg}", file=sys.stderr)


def resolve_bot_user_id() -> str:
    """Call auth.test to discover this bot's Slack user ID.

    Returns the user ID string (e.g. "U0123ABC") or empty string on failure.
    """
    if not SLACK_BOT_TOKEN:
        return ""

    with httpx.Client() as client:
        response = client.post(
            "https://slack.com/api/auth.test",
            headers={"Authorization": f"Bearer {SLACK_BOT_TOKEN}"},
        )
        data = response.json()
        if data.get("ok"):
            return data.get("user_id", "")
        log(f"auth.test failed: {data.get('error', 'unknown')}")
        return ""


def send_slack_message(
    text: str,
    thread_ts: str | None = None,
    reply_broadcast: bool = False,
) -> dict[str, Any]:
    """Send a message to Slack.

    Args:
        text: Message text.
        thread_ts: If set, post as a reply in this thread.
        reply_broadcast: When True (and thread_ts is set), also sends the reply
            to the main channel feed so it is visible outside the thread.
    """
    if not SLACK_BOT_TOKEN:
        return {"error": "SLACK_BOT_TOKEN not configured"}
    if not SLACK_CHANNEL_ID:
        return {"error": "SLACK_CHANNEL_ID not configured"}

    with httpx.Client() as client:
        payload: dict[str, Any] = {
            "channel": SLACK_CHANNEL_ID,
            "text": text,
        }
        if thread_ts:
            payload["thread_ts"] = thread_ts
            if reply_broadcast:
                payload["reply_broadcast"] = True

        response = client.post(
            "https://slack.com/api/chat.postMessage",
            headers={"Authorization": f"Bearer {SLACK_BOT_TOKEN}"},
            json=payload,
        )
        return response.json()


def get_thread_replies_after(
    thread_ts: str,
    after_ts: str,
    timeout_seconds: int = 300,
) -> str | None:
    """Poll for human replies in a Slack thread posted after a given timestamp.

    Filters out bot messages (identified by the presence of a ``bot_id`` field)
    and — when the bot's user ID is known — requires the reply to contain an
    ``@``-mention of the bot (``<@BOT_USER_ID>``).  This prevents the agent
    from reacting to unrelated chatter in the thread.

    Args:
        thread_ts: The timestamp of the root thread message.
        after_ts: Only consider messages with a timestamp strictly greater than
            this value (typically the ts of the question message itself).
        timeout_seconds: How long to wait before giving up.
    """
    if not SLACK_BOT_TOKEN:
        return None

    mention_tag = f"<@{SLACK_BOT_USER_ID}>" if SLACK_BOT_USER_ID else ""

    start_time = time.time()

    with httpx.Client() as client:
        while time.time() - start_time < timeout_seconds:
            response = client.get(
                "https://slack.com/api/conversations.replies",
                headers={"Authorization": f"Bearer {SLACK_BOT_TOKEN}"},
                params={"channel": SLACK_CHANNEL_ID, "ts": thread_ts},
            )
            data = response.json()

            if data.get("ok") and data.get("messages"):
                for msg in data["messages"]:
                    msg_ts = msg.get("ts", "0")
                    if msg_ts <= after_ts or "bot_id" in msg:
                        continue
                    text = msg.get("text", "")
                    # When we know the bot's user ID, only accept replies
                    # that explicitly @-mention us.
                    if mention_tag and mention_tag not in text:
                        continue
                    return text

            time.sleep(5)  # Poll every 5 seconds

    return None


def handle_ask_human(question: str) -> dict[str, Any]:
    """Ask a question to humans via Slack and wait for response.

    When SLACK_THREAD_TS is set, the question is posted as a reply in the
    existing task thread so that the entire conversation is grouped together.
    Otherwise a new top-level message is created and the reply is awaited in
    its own thread (legacy behaviour).
    """
    log(f"Asking human: {question}")

    if SLACK_BOT_USER_ID:
        reply_hint = f"_Tag <@{SLACK_BOT_USER_ID}> in your reply so I can see it._"
    else:
        reply_hint = "_Reply in this thread to respond._"

    question_text = f"🤖 *Osmia needs your input:*\n\n{question}\n\n{reply_hint}"

    if SLACK_THREAD_TS:
        # Post the question as a reply in the main task thread.
        result = send_slack_message(question_text, thread_ts=SLACK_THREAD_TS)
    else:
        # Legacy path: create a new top-level message with its own thread.
        result = send_slack_message(question_text)

    if not result.get("ok"):
        return {"error": f"Failed to post to Slack: {result.get('error', 'unknown')}"}

    question_ts = result.get("ts")
    if not question_ts:
        return {"error": "No message timestamp returned from Slack"}

    # Determine which thread to poll for replies.
    poll_thread_ts = SLACK_THREAD_TS if SLACK_THREAD_TS else question_ts

    log(f"Posted question (ts={question_ts}), polling thread {poll_thread_ts} for reply")

    reply = get_thread_replies_after(poll_thread_ts, after_ts=question_ts, timeout_seconds=300)

    if reply:
        log(f"Got reply: {reply}")
        send_slack_message("Ok! Thanks for your response.", thread_ts=poll_thread_ts)
        return {"answer": reply}
    else:
        return {"error": "Timeout waiting for human response (5 minutes)"}


def handle_notify_human(message: str) -> dict[str, Any]:
    """Send a notification to humans via Slack (no response expected).

    When SLACK_THREAD_TS is set, the notification is posted as a reply in the
    existing task thread.
    """
    log(f"Notifying human: {message}")

    notification_text = f"📢 *Osmia notification:*\n\n{message}"
    thread_ts = SLACK_THREAD_TS if SLACK_THREAD_TS else None
    result = send_slack_message(notification_text, thread_ts=thread_ts)

    if result.get("ok"):
        return {"status": "sent"}
    else:
        return {"error": f"Failed to send: {result.get('error', 'unknown')}"}


def handle_notify_start(story_id: str, story_title: str) -> dict[str, Any]:
    """Post a planning update to Slack when Osmia begins analysing a story.

    When SLACK_THREAD_TS is set (injected by the controller), this is posted as
    a threaded reply under the controller's initial "started working on" message
    to avoid a duplicate top-level notification. The message is intentionally
    different from the controller's message — it signals that the agent has
    actually started reading the codebase and planning its approach.
    """
    log(f"Starting work on story {story_id}: {story_title}")

    message = (
        f"🔍 Analysing the codebase and planning approach for sc-{story_id}…\n\n*{story_title}*"
    )

    if SLACK_THREAD_TS:
        result = send_slack_message(message, thread_ts=SLACK_THREAD_TS)
    else:
        result = send_slack_message(message)

    if result.get("ok"):
        return {"status": "sent", "story_id": story_id}
    else:
        return {"error": f"Failed to send: {result.get('error', 'unknown')}"}


# =============================================================================
# GitLab / CodeRabbit Functions
# =============================================================================


def parse_mr_url(mr_url: str) -> tuple[str, int] | None:
    """Parse a GitLab MR URL to extract project path and MR IID.

    Args:
        mr_url: Full GitLab MR URL (e.g., https://gitlab.com/group/project/-/merge_requests/123)

    Returns:
        Tuple of (project_path, mr_iid) or None if invalid URL
    """
    pattern = r"gitlab\.com/(.+?)/-/merge_requests/(\d+)"
    match = re.search(pattern, mr_url)
    if not match:
        return None

    project_path = match.group(1)
    mr_iid = int(match.group(2))
    return project_path, mr_iid


def encode_project_path(project_path: str) -> str:
    """URL-encode a project path for GitLab API."""
    return project_path.replace("/", "%2F")


def gitlab_request(method: str, endpoint: str, **kwargs: Any) -> dict[str, Any] | list | None:
    """Make an authenticated request to GitLab API."""
    if not GITLAB_TOKEN:
        return None

    with httpx.Client(timeout=30.0) as client:
        response = client.request(
            method,
            f"{GITLAB_API_BASE}{endpoint}",
            headers={"PRIVATE-TOKEN": GITLAB_TOKEN},
            **kwargs,
        )
        if response.status_code >= 400:
            log(f"GitLab API error {response.status_code}: {response.text}")
            return None
        return response.json()


def get_mr_pipelines(mr_url: str) -> list[dict] | None:
    """Get pipelines associated with a merge request."""
    parsed = parse_mr_url(mr_url)
    if not parsed:
        return None

    project_path, mr_iid = parsed
    encoded_project = encode_project_path(project_path)

    result = gitlab_request("GET", f"/projects/{encoded_project}/merge_requests/{mr_iid}/pipelines")
    if isinstance(result, list):
        return result
    return None


def get_pipeline_status(mr_url: str) -> str | None:
    """Get the status of the latest pipeline for an MR."""
    pipelines = get_mr_pipelines(mr_url)
    if not pipelines:
        return None

    # Get the most recent pipeline (first in list)
    latest = pipelines[0]
    return latest.get("status")


def get_mr_notes(mr_url: str) -> list[dict] | None:
    """Get all notes (comments) for a merge request."""
    parsed = parse_mr_url(mr_url)
    if not parsed:
        return None

    project_path, mr_iid = parsed
    encoded_project = encode_project_path(project_path)

    result = gitlab_request(
        "GET",
        f"/projects/{encoded_project}/merge_requests/{mr_iid}/notes",
        params={"per_page": 100},
    )
    if isinstance(result, list):
        return result
    return None


def extract_severity(body: str) -> str:
    """Extract severity from CodeRabbit comment body."""
    body_lower = body.lower()

    if any(ind in body_lower for ind in ["🔴", "[critical]", "critical:", "severity: critical"]):
        return "critical"
    elif any(ind in body_lower for ind in ["🟠", "[major]", "major:", "severity: major"]):
        return "major"
    elif any(ind in body_lower for ind in ["🟡", "[minor]", "minor:", "severity: minor"]):
        return "minor"
    elif any(ind in body_lower for ind in ["🟢", "[nitpick]", "nitpick:", "severity: nitpick"]):
        return "nitpick"
    return "unknown"


def is_comment_addressed(body: str, resolved: bool) -> bool:
    """Check if a comment has been addressed."""
    if resolved:
        return True

    addressed_patterns = [r"addressed in commit", r"fixed in", r"✅"]
    body_lower = body.lower()
    return any(re.search(pattern, body_lower) for pattern in addressed_patterns)


def get_coderabbit_comments(mr_url: str) -> list[dict]:
    """Get CodeRabbit comments from a merge request."""
    notes = get_mr_notes(mr_url)
    if not notes:
        return []

    comments = []
    for note in notes:
        # Check if this is a CodeRabbit comment
        author = note.get("author", {})
        username = author.get("username", "")

        if CODERABBIT_BOT_NAME not in username.lower():
            continue

        body = note.get("body", "")
        resolved = note.get("resolved", False)

        # Extract position info if available
        position = note.get("position", {})
        path = position.get("new_path") or position.get("old_path")
        line = position.get("new_line") or position.get("old_line")

        severity = extract_severity(body)
        addressed = is_comment_addressed(body, resolved)

        comments.append({
            "id": note["id"],
            "body": body,
            "path": path,
            "line": line,
            "severity": severity,
            "resolved": resolved,
            "addressed": addressed,
            "is_actionable": severity in ("critical", "major"),
        })

    return comments


def handle_wait_for_pipeline(mr_url: str, timeout_seconds: int = 600) -> dict[str, Any]:
    """Wait for the CI pipeline to complete."""
    log(f"Waiting for pipeline on {mr_url} (timeout: {timeout_seconds}s)")

    if not GITLAB_TOKEN:
        return {"error": "GITLAB_TOKEN not configured"}

    parsed = parse_mr_url(mr_url)
    if not parsed:
        return {"error": f"Invalid MR URL: {mr_url}"}

    final_statuses = {"success", "failed", "canceled", "skipped"}
    elapsed = 0
    poll_interval = 30

    while elapsed < timeout_seconds:
        status = get_pipeline_status(mr_url)

        if status is None:
            log(f"No pipeline found yet, waiting... ({elapsed}s elapsed)")
        elif status in final_statuses:
            log(f"Pipeline completed with status: {status}")
            success = status == "success"
            return {
                "success": success,
                "status": status,
                "message": f"Pipeline {status}" if success else f"Pipeline failed with status: {status}",
            }
        else:
            log(f"Pipeline status: {status}, waiting... ({elapsed}s elapsed)")

        time.sleep(poll_interval)
        elapsed += poll_interval

    return {
        "success": False,
        "status": "timeout",
        "message": f"Pipeline did not complete within {timeout_seconds} seconds",
    }


def handle_wait_for_coderabbit(mr_url: str, timeout_seconds: int = 300) -> dict[str, Any]:
    """Wait for CodeRabbit to review an MR and return actionable comments."""
    log(f"Waiting for CodeRabbit review on {mr_url} (timeout: {timeout_seconds}s)")

    if not GITLAB_TOKEN:
        return {"error": "GITLAB_TOKEN not configured"}

    parsed = parse_mr_url(mr_url)
    if not parsed:
        return {"error": f"Invalid MR URL: {mr_url}"}

    elapsed = 0
    poll_interval = 30

    # First wait a bit for CodeRabbit to start reviewing
    initial_wait = min(30, timeout_seconds)
    log(f"Initial wait of {initial_wait}s for CodeRabbit to start...")
    time.sleep(initial_wait)
    elapsed += initial_wait

    while elapsed < timeout_seconds:
        comments = get_coderabbit_comments(mr_url)

        if comments:
            # Get actionable unaddressed comments
            actionable = [c for c in comments if not c["addressed"] and c["is_actionable"]]
            log(f"CodeRabbit review found: {len(comments)} total, {len(actionable)} actionable")

            return {
                "review_complete": True,
                "total_comments": len(comments),
                "actionable_count": len(actionable),
                "actionable_comments": actionable,
                "message": (
                    f"CodeRabbit found {len(actionable)} actionable comments"
                    if actionable
                    else "CodeRabbit review complete, no actionable comments"
                ),
            }

        log(f"No CodeRabbit comments yet, waiting... ({elapsed}s elapsed)")
        time.sleep(poll_interval)
        elapsed += poll_interval

    log(f"CodeRabbit review timeout after {timeout_seconds}s - no comments found")
    return {
        "review_complete": True,
        "total_comments": 0,
        "actionable_count": 0,
        "actionable_comments": [],
        "message": "No CodeRabbit comments found (review may not have triggered)",
    }


def handle_get_coderabbit_comments(mr_url: str) -> dict[str, Any]:
    """Get current CodeRabbit comments for an MR."""
    log(f"Getting CodeRabbit comments for {mr_url}")

    if not GITLAB_TOKEN:
        return {"error": "GITLAB_TOKEN not configured"}

    parsed = parse_mr_url(mr_url)
    if not parsed:
        return {"error": f"Invalid MR URL: {mr_url}"}

    comments = get_coderabbit_comments(mr_url)
    actionable = [c for c in comments if not c["addressed"] and c["is_actionable"]]

    return {
        "total": len(comments),
        "actionable": len(actionable),
        "comments": comments,
        "actionable_comments": actionable,
    }


def handle_request(request: dict[str, Any]) -> dict[str, Any]:
    """Handle a JSON-RPC request."""
    method = request.get("method", "")
    params = request.get("params", {})
    request_id = request.get("id")

    if method == "initialize":
        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "osmia-slack", "version": "1.0.0"},
            },
        }

    elif method == "tools/list":
        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "result": {
                "tools": [
                    {
                        "name": "ask_human",
                        "description": "Ask a question to humans via Slack and wait for their response. Use this when you need clarification or approval.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "question": {
                                    "type": "string",
                                    "description": "The question to ask the human",
                                }
                            },
                            "required": ["question"],
                        },
                    },
                    {
                        "name": "notify_human",
                        "description": "Send a notification to humans via Slack. Use this for status updates or completion messages.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "message": {
                                    "type": "string",
                                    "description": "The message to send",
                                }
                            },
                            "required": ["message"],
                        },
                    },
                    {
                        "name": "notify_start",
                        "description": "Notify humans that Osmia is starting work on a Shortcut story. Call this at the beginning of each task.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "story_id": {
                                    "type": "string",
                                    "description": "The Shortcut story ID (e.g., '12345')",
                                },
                                "story_title": {
                                    "type": "string",
                                    "description": "The title of the story being worked on",
                                },
                            },
                            "required": ["story_id", "story_title"],
                        },
                    },
                    {
                        "name": "wait_for_pipeline",
                        "description": "Wait for the GitLab CI pipeline to complete on a merge request. Returns success/failure status. Call this after creating an MR to ensure CI passes before proceeding.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "mr_url": {
                                    "type": "string",
                                    "description": "The full GitLab merge request URL (e.g., 'https://gitlab.com/group/project/-/merge_requests/123')",
                                },
                                "timeout_seconds": {
                                    "type": "integer",
                                    "description": "Maximum time to wait in seconds (default: 600)",
                                    "default": 600,
                                },
                            },
                            "required": ["mr_url"],
                        },
                    },
                    {
                        "name": "wait_for_coderabbit_review",
                        "description": "Wait for CodeRabbit to review a merge request and return any actionable comments (Critical or Major severity). Call this after the pipeline passes to get review feedback.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "mr_url": {
                                    "type": "string",
                                    "description": "The full GitLab merge request URL",
                                },
                                "timeout_seconds": {
                                    "type": "integer",
                                    "description": "Maximum time to wait in seconds (default: 300)",
                                    "default": 300,
                                },
                            },
                            "required": ["mr_url"],
                        },
                    },
                    {
                        "name": "get_coderabbit_comments",
                        "description": "Get current CodeRabbit comments on a merge request. Use this to check if previously actionable comments have been addressed after pushing fixes.",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "mr_url": {
                                    "type": "string",
                                    "description": "The full GitLab merge request URL",
                                },
                            },
                            "required": ["mr_url"],
                        },
                    },
                ]
            },
        }

    elif method == "tools/call":
        tool_name = params.get("name", "")
        tool_args = params.get("arguments", {})

        if tool_name == "ask_human":
            result = handle_ask_human(tool_args.get("question", ""))
        elif tool_name == "notify_human":
            result = handle_notify_human(tool_args.get("message", ""))
        elif tool_name == "notify_start":
            result = handle_notify_start(
                tool_args.get("story_id", ""),
                tool_args.get("story_title", ""),
            )
        elif tool_name == "wait_for_pipeline":
            result = handle_wait_for_pipeline(
                tool_args.get("mr_url", ""),
                tool_args.get("timeout_seconds", 600),
            )
        elif tool_name == "wait_for_coderabbit_review":
            result = handle_wait_for_coderabbit(
                tool_args.get("mr_url", ""),
                tool_args.get("timeout_seconds", 300),
            )
        elif tool_name == "get_coderabbit_comments":
            result = handle_get_coderabbit_comments(tool_args.get("mr_url", ""))
        else:
            result = {"error": f"Unknown tool: {tool_name}"}

        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "result": {"content": [{"type": "text", "text": json.dumps(result)}]},
        }

    elif method == "notifications/initialized":
        # This is a notification, no response needed
        return None

    else:
        return {
            "jsonrpc": "2.0",
            "id": request_id,
            "error": {"code": -32601, "message": f"Method not found: {method}"},
        }


def main() -> None:
    """Main loop - read JSON-RPC requests from stdin, write responses to stdout."""
    global SLACK_BOT_USER_ID  # noqa: PLW0603

    log("Slack/GitLab MCP server starting")
    log(f"SLACK_BOT_TOKEN configured: {bool(SLACK_BOT_TOKEN)}")
    log(f"SLACK_CHANNEL_ID: {SLACK_CHANNEL_ID}")
    log(f"SLACK_THREAD_TS: {SLACK_THREAD_TS or '(not set — messages will be top-level)'}")
    log(f"GITLAB_TOKEN configured: {bool(GITLAB_TOKEN)}")

    SLACK_BOT_USER_ID = resolve_bot_user_id()
    if SLACK_BOT_USER_ID:
        log(f"Resolved bot user ID: {SLACK_BOT_USER_ID}")
    else:
        log("Could not resolve bot user ID — reply filtering will accept any non-bot message")

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            request = json.loads(line)
            log(f"Received: {request.get('method', 'unknown')}")

            response = handle_request(request)

            if response is not None:
                print(json.dumps(response), flush=True)

        except json.JSONDecodeError as e:
            log(f"JSON decode error: {e}")
            error_response = {
                "jsonrpc": "2.0",
                "id": None,
                "error": {"code": -32700, "message": "Parse error"},
            }
            print(json.dumps(error_response), flush=True)
        except Exception as e:
            log(f"Error: {e}")


if __name__ == "__main__":
    main()
