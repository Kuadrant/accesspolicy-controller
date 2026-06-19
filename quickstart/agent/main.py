import os

from google.adk.cli.fast_api import get_fast_api_app

APP_NAME = os.environ.get("APP_NAME", "mcp_agent")
SESSION_DB_URL = os.environ.get("SESSION_DB_URL", "")
ALLOWED_ORIGINS = os.environ.get("ALLOWED_ORIGINS", "*")

app = get_fast_api_app(
    agents_dir=".",
    session_db_url=SESSION_DB_URL,
    allow_origins=ALLOWED_ORIGINS.split(","),
    web=True,
)
