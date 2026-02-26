# Guard Rails

## Never Do
- Never modify CI/CD pipeline configuration files
- Never change database migration files
- Never alter authentication or authorisation logic
- Never commit secrets, API keys, or credentials
- Never modify files in the .github/ directory
- Never run destructive commands (rm -rf, DROP TABLE, etc)

## Always Do
- Always run the full test suite before creating a pull request
- Always add tests for new functionality
- Always follow the existing code style in the repository
- Always create a new branch for changes (never push to main)
- Always include a clear summary in the pull request description
