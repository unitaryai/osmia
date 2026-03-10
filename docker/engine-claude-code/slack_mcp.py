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

# GitLab configuration from environment
GITLAB_TOKEN = os.environ.get("GITLAB_TOKEN", "")
GITLAB_API_BASE = "https://gitlab.com/api/v4"

# CodeRabbit bot username
CODERABBIT_BOT_NAME = "coderabbitai"


def log(msg: str) -> None:
    """Log to stderr (stdout is reserved for MCP protocol)."""
    print(f"[slack-mcp] {msg}", file=sys.stderr)


def send_slack_message(text: str, thread_ts: str | None = None) -> dict[str, Any]:
    """Send a message to Slack."""
    if not SLACK_BOT_TOKEN:
        return {"error": "SLACK_BOT_TOKEN not configured"}
    if not SLACK_CHANNEL_ID:
        return {"error": "SLACK_CHANNEL_ID not configured"}

    with httpx.Client() as client:
        payload = {
            "channel": SLACK_CHANNEL_ID,
            "text": text,
        }
        if thread_ts:
            payload["thread_ts"] = thread_ts

        response = client.post(
            "https://slack.com/api/chat.postMessage",
            headers={"Authorization": f"Bearer {SLACK_BOT_TOKEN}"},
            json=payload,
        )
        return response.json()


def get_thread_replies(thread_ts: str, timeout_seconds: int = 300) -> str | None:
    """Poll for replies in a Slack thread."""
    if not SLACK_BOT_TOKEN:
        return None

    start_time = time.time()
    last_reply_count = 0

    with httpx.Client() as client:
        while time.time() - start_time < timeout_seconds:
            response = client.get(
                "https://slack.com/api/conversations.replies",
                headers={"Authorization": f"Bearer {SLACK_BOT_TOKEN}"},
                params={"channel": SLACK_CHANNEL_ID, "ts": thread_ts},
            )
            data = response.json()

            if data.get("ok") and data.get("messages"):
                messages = data["messages"]
                # Skip the first message (the question itself)
                replies = messages[1:] if len(messages) > 1 else []

                if len(replies) > last_reply_count:
                    # New reply received
                    latest_reply = replies[-1]
                    return latest_reply.get("text", "")

                last_reply_count = len(replies)

            time.sleep(5)  # Poll every 5 seconds

    return None


def handle_ask_human(question: str) -> dict[str, Any]:
    """Ask a question to humans via Slack and wait for response."""
    log(f"Asking human: {question}")

    # Post the question
    result = send_slack_message(f"🤖 *Osmia needs your input:*\n\n{question}")

    if not result.get("ok"):
        return {"error": f"Failed to post to Slack: {result.get('error', 'unknown')}"}

    thread_ts = result.get("ts")
    if not thread_ts:
        return {"error": "No thread timestamp returned"}

    log(f"Posted question, waiting for reply in thread {thread_ts}")

    # Wait for reply
    reply = get_thread_replies(thread_ts, timeout_seconds=300)

    if reply:
        log(f"Got reply: {reply}")
        # Acknowledge the response
        send_slack_message("Ok! Thanks for your response.", thread_ts=thread_ts)
        return {"answer": reply}
    else:
        return {"error": "Timeout waiting for human response (5 minutes)"}


def handle_notify_human(message: str) -> dict[str, Any]:
    """Send a notification to humans via Slack (no response expected)."""
    log(f"Notifying human: {message}")

    result = send_slack_message(f"📢 *Osmia notification:*\n\n{message}")

    if result.get("ok"):
        return {"status": "sent"}
    else:
        return {"error": f"Failed to send: {result.get('error', 'unknown')}"}


def handle_notify_start(story_id: str, story_title: str) -> dict[str, Any]:
    """Notify Slack that Osmia is starting work on a story."""
    log(f"Starting work on story {story_id}: {story_title}")

    message = f"🚀 *Osmia is starting work on sc-{story_id}*\n\n*{story_title}*"
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
    log("Slack/GitLab MCP server starting")
    log(f"SLACK_BOT_TOKEN configured: {bool(SLACK_BOT_TOKEN)}")
    log(f"SLACK_CHANNEL_ID: {SLACK_CHANNEL_ID}")
    log(f"GITLAB_TOKEN configured: {bool(GITLAB_TOKEN)}")

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
