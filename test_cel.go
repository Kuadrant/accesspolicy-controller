package main

import (
	"fmt"
	"regexp"
	"strings"
)

func main() {
	exprs := []string{
		"request.mcp.tool_name == 'get-sum'",
		"request.mcp.tool_name == 'echo'",
		"!(has(request.http.headers) && 'x-mcp-toolname' in request.http.headers) || request.http.headers['x-mcp-toolname'] == ''",
	}

	re := regexp.MustCompile(`request\.mcp\.tool_name\s*==\s*'([^']+)'`)

	for _, expr := range exprs {
		if strings.Contains(expr, "== ''") {
			fmt.Printf("Empty match for %s\n", expr)
			continue
		}

		matches := re.FindStringSubmatch(expr)
		if len(matches) > 1 {
			fmt.Printf("Tool match %s for %s\n", matches[1], expr)
		} else {
			fmt.Printf("No match for %s\n", expr)
		}
	}
}
