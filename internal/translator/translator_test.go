package translator

import (
	"testing"
)

func TestTranslateCEL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "substitute tool_name",
			input:    "request.mcp.tool_name == 'search_web'",
			expected: "('x-mcp-toolname' in request.headers ? request.headers['x-mcp-toolname'] : '') == 'search_web'",
		},
		{
			name:     "multiple substitutions",
			input:    "request.mcp.tool_name == 'search_web' || request.mcp.tool_name == 'read_file'",
			expected: "('x-mcp-toolname' in request.headers ? request.headers['x-mcp-toolname'] : '') == 'search_web' || ('x-mcp-toolname' in request.headers ? request.headers['x-mcp-toolname'] : '') == 'read_file'",
		},
		{
			name:     "no substitution needed",
			input:    "request.headers['authorization'] != ''",
			expected: "request.headers['authorization'] != ''",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TranslateCEL(tt.input)
			if result != tt.expected {
				t.Errorf("TranslateCEL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestValidateCEL(t *testing.T) {
	tests := []struct {
		name        string
		expression  string
		expectError bool
	}{
		{
			name:        "valid syntax with headers",
			expression:  "request.headers['x-mcp-toolname'] == 'search_web'",
			expectError: false,
		},
		{
			name:        "invalid syntax",
			expression:  "request.headers['x-mcp-toolname'] ==",
			expectError: true, // syntax error
		},
		{
			name:        "valid syntax with missing runtime fields",
			expression:  "request.non_existent_field == 'something'",
			expectError: false, // Compiles fine, it's syntactically valid CEL
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCEL(tt.expression)
			if tt.expectError && err == nil {
				t.Errorf("ValidateCEL(%q) expected error, got nil", tt.expression)
			}
			if !tt.expectError && err != nil {
				t.Errorf("ValidateCEL(%q) unexpected error: %v", tt.expression, err)
			}
		})
	}
}
