FROM python:3.12-slim

WORKDIR /app

COPY pyproject.toml .
COPY robodev_plugin_jira/ robodev_plugin_jira/

RUN pip install --no-cache-dir .

USER nobody

ENTRYPOINT ["python", "-m", "robodev_plugin_jira"]
