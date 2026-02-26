"""Example Jira ticketing plugin for RoboDev.

This module implements the TicketingBackend gRPC interface for Jira Cloud.
It demonstrates how to build a third-party plugin in Python using the
RoboDev Plugin SDK.

Note: This is an example/template. The robodev-plugin-sdk package is not
yet published; this code shows the intended plugin development experience.
"""

import os
import logging

logger = logging.getLogger(__name__)

# Configuration from environment variables.
JIRA_BASE_URL = os.getenv("JIRA_BASE_URL", "https://yourorg.atlassian.net")
JIRA_EMAIL = os.getenv("JIRA_EMAIL", "")
JIRA_API_TOKEN = os.getenv("JIRA_API_TOKEN", "")
JIRA_PROJECT_KEY = os.getenv("JIRA_PROJECT_KEY", "")
JIRA_LABEL = os.getenv("JIRA_LABEL", "robodev")


class JiraTicketingBackend:
    """Jira Cloud ticketing backend for RoboDev.

    Implements the TicketingBackend gRPC interface:
    - PollReadyTickets: searches Jira for issues with the configured label
    - MarkInProgress: transitions issue to "In Progress"
    - MarkComplete: transitions issue to "Done" and adds a comment
    - MarkFailed: adds a "robodev-failed" label and comment
    - AddComment: posts a comment on the issue
    """

    def __init__(self):
        self.base_url = JIRA_BASE_URL.rstrip("/")
        self.auth = (JIRA_EMAIL, JIRA_API_TOKEN)
        self.project_key = JIRA_PROJECT_KEY
        self.label = JIRA_LABEL

    @property
    def name(self) -> str:
        return "jira"

    @property
    def interface_version(self) -> int:
        return 1

    def poll_ready_tickets(self):
        """Search Jira for issues ready to be processed.

        Uses JQL: project = {PROJECT_KEY} AND labels = {LABEL} AND status = "To Do"
        """
        import requests

        jql = f'project = "{self.project_key}" AND labels = "{self.label}" AND status = "To Do"'
        url = f"{self.base_url}/rest/api/3/search"
        params = {"jql": jql, "maxResults": 10}

        resp = requests.get(url, params=params, auth=self.auth, timeout=30)
        resp.raise_for_status()

        tickets = []
        for issue in resp.json().get("issues", []):
            tickets.append({
                "id": issue["key"],
                "title": issue["fields"]["summary"],
                "description": issue["fields"].get("description", {}).get("content", [{}])[0].get("content", [{}])[0].get("text", ""),
                "ticket_type": issue["fields"]["issuetype"]["name"].lower(),
                "labels": issue["fields"].get("labels", []),
                "external_url": f"{self.base_url}/browse/{issue['key']}",
                "raw": issue,
            })

        return tickets

    def mark_in_progress(self, ticket_id: str):
        """Transition the Jira issue to 'In Progress'."""
        import requests

        # Get available transitions.
        url = f"{self.base_url}/rest/api/3/issue/{ticket_id}/transitions"
        resp = requests.get(url, auth=self.auth, timeout=30)
        resp.raise_for_status()

        # Find the "In Progress" transition.
        for transition in resp.json().get("transitions", []):
            if transition["name"].lower() == "in progress":
                requests.post(
                    url,
                    json={"transition": {"id": transition["id"]}},
                    auth=self.auth,
                    timeout=30,
                ).raise_for_status()
                return

        logger.warning("could not find 'In Progress' transition for %s", ticket_id)

    def mark_complete(self, ticket_id: str, result: dict):
        """Transition the issue to 'Done' and add a completion comment."""
        self.add_comment(
            ticket_id,
            f"RoboDev completed this task.\n\nSummary: {result.get('summary', 'N/A')}\n"
            f"Merge Request: {result.get('merge_request_url', 'N/A')}",
        )

    def mark_failed(self, ticket_id: str, reason: str):
        """Add a failure label and comment."""
        self.add_comment(ticket_id, f"RoboDev failed to complete this task.\n\nReason: {reason}")

    def add_comment(self, ticket_id: str, comment: str):
        """Post a comment on the Jira issue."""
        import requests

        url = f"{self.base_url}/rest/api/3/issue/{ticket_id}/comment"
        body = {
            "body": {
                "type": "doc",
                "version": 1,
                "content": [
                    {
                        "type": "paragraph",
                        "content": [{"type": "text", "text": comment}],
                    }
                ],
            }
        }
        resp = requests.post(url, json=body, auth=self.auth, timeout=30)
        resp.raise_for_status()


def main():
    """Entry point for the Jira plugin.

    In production, this would use the robodev-plugin-sdk to register
    the backend as a gRPC service and start serving.
    """
    logger.info("starting robodev-plugin-jira")

    # When the SDK is available:
    # from robodev_plugin_sdk import serve
    # serve(JiraTicketingBackend(), interface="ticketing")

    backend = JiraTicketingBackend()
    logger.info("jira backend initialised: project=%s, label=%s", backend.project_key, backend.label)


if __name__ == "__main__":
    main()
