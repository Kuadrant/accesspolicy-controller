import os

from google.adk.agents import LlmAgent
from google.adk.tools.mcp_tool.mcp_toolset import McpToolset
from google.adk.tools.mcp_tool.mcp_session_manager import StreamableHTTPConnectionParams

GATEWAY_URL = os.environ.get("GATEWAY_URL", "http://localhost:8080")
LLM_MODEL = os.environ.get("LLM_MODEL", "gemini-2.0-flash")

mcp_toolset = McpToolset(
    connection_params=StreamableHTTPConnectionParams(url=GATEWAY_URL),
)

root_agent = LlmAgent(
    name="mcp_agent",
    model=LLM_MODEL,
    instruction=(
        "You are a helpful assistant with access to MCP tools. "
        "Use the available tools to help the user. "
        "When a tool call fails or is denied, explain that to the user."
    ),
    tools=[mcp_toolset],
)
