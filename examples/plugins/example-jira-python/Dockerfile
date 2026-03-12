FROM python:3.12-slim

WORKDIR /app

COPY pyproject.toml .
COPY osmia_plugin_jira/ osmia_plugin_jira/

RUN pip install --no-cache-dir .

USER nobody

ENTRYPOINT ["python", "-m", "osmia_plugin_jira"]
