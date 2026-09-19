package lang

import (
	"fmt"
	"strings"
)

// ParseError is a structured error type for DSL parse and compile errors.
// It carries source location and the offending source line so the CLI can
// render rich diagnostic output with a caret pointing at the error column.
type ParseError struct {
	File       string // source filename (empty if not known)
	Line       int    // 1-based line number
	Col        int    // 1-based column number (0 if not known)
	Message    string // human-readable error description
	SourceLine string // the full text of the offending source line
}

// Error formats the error with source context. When a source line is available,
// it renders the line followed by a caret (^) pointing at the error column.
func (parseError *ParseError) Error() string {
	var builder strings.Builder

	if parseError.File != "" {
		fmt.Fprintf(&builder, "%s:", parseError.File)
	}

	fmt.Fprintf(&builder, "%d", parseError.Line)
	if parseError.Col > 0 {
		fmt.Fprintf(&builder, ":%d", parseError.Col)
	}
	fmt.Fprintf(&builder, ": %s", parseError.Message)

	if parseError.SourceLine != "" {
		builder.WriteByte('\n')
		builder.WriteString("  ")
		builder.WriteString(parseError.SourceLine)
		if parseError.Col > 0 {
			builder.WriteByte('\n')
			builder.WriteString("  ")
			builder.WriteString(strings.Repeat(" ", parseError.Col-1))
			builder.WriteByte('^')
		}
	}

	return builder.String()
}

// splitSourceLines splits the source text into individual lines for error
// context display. The returned slice is 0-indexed (line 1 is at index 0).
func splitSourceLines(sourceText string) []string {
	return strings.Split(sourceText, "\n")
}

// sourceLineAt returns the source line at the given 1-based line number,
// or empty string if out of range.
func sourceLineAt(sourceLines []string, lineNumber int) string {
	if lineNumber < 1 || lineNumber > len(sourceLines) {
		return ""
	}
	return sourceLines[lineNumber-1]
}
