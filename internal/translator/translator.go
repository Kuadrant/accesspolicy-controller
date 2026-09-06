package translator

import (
	"strings"

	"github.com/google/cel-go/cel"
)

// TranslateCEL translates domain-specific MCP variables into data-plane variables.
func TranslateCEL(expr string) string {
	res := expr
	safeToolName := "(has(request.headers) && 'x-mcp-toolname' in request.headers ? request.headers['x-mcp-toolname'] : '')"
	safeMethod := "(has(request.headers) && 'x-mcp-method' in request.headers ? request.headers['x-mcp-method'] : '')"
	res = strings.ReplaceAll(res, "request.mcp.tool_name", safeToolName)
	res = strings.ReplaceAll(res, "request.mcp.toolName", safeToolName)
	res = strings.ReplaceAll(res, "request.mcp.toolname", safeToolName)
	res = strings.ReplaceAll(res, "request.mcp.method", safeMethod)
	return res
}

// ValidateCEL checks if the CEL expression has valid syntax.
// We only perform syntax validation, not semantic/type validation.
func ValidateCEL(expr string) error {
	// Initialize a minimal CEL environment
	env, err := cel.NewEnv()
	if err != nil {
		return err
	}

	ast, iss := env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return iss.Err()
	}
	_ = ast

	return nil
}
