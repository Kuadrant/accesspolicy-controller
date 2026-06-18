package translator

import (
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
)

// TranslateCEL converts domain-specific MCP variables to the Envoy AuthZ header context
func TranslateCEL(expression string) string {
	// Replace abstract tool_name with the header injected by mcp-broker-router
	expr := strings.ReplaceAll(expression, "request.mcp.tool_name", "request.headers['x-mcp-toolname']")
	return expr
}

var (
	celEnv     *cel.Env
	celEnvErr  error
	celEnvOnce sync.Once
)

// getCelEnv initializes a CEL environment loosely mirroring the Envoy AuthZ context
func getCelEnv() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		// Authorino's Envoy Context uses "request" as the base object
		celEnv, celEnvErr = cel.NewEnv(
			cel.Variable("request", cel.MapType(cel.StringType, cel.AnyType)),
			cel.Variable("auth", cel.MapType(cel.StringType, cel.AnyType)),
			ext.Strings(),
		)
	})
	return celEnv, celEnvErr
}

// ValidateCEL compiles the expression to verify syntactic correctness
func ValidateCEL(expression string) error {
	env, err := getCelEnv()
	if err != nil {
		return err // Internal error initializing environment
	}

	_, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return issues.Err() // Return syntax error for the controller to mark as InvalidCEL
	}

	return nil
}
