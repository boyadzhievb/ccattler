package lang

import (
	"errors"
	"strings"
	"testing"
)

func TestParseErrorFormatWithSourceContext(t *testing.T) {
	parseError := &ParseError{
		Line:       3,
		Col:        5,
		Message:    "unknown service field \"bogus\"",
		SourceLine: "    bogus 42",
	}

	errorText := parseError.Error()

	if !strings.Contains(errorText, "3:5:") {
		t.Errorf("expected line:col prefix, got: %s", errorText)
	}
	if !strings.Contains(errorText, "unknown service field \"bogus\"") {
		t.Errorf("expected error message, got: %s", errorText)
	}
	if !strings.Contains(errorText, "    bogus 42") {
		t.Errorf("expected source line, got: %s", errorText)
	}
	if !strings.Contains(errorText, "    ^") {
		t.Errorf("expected caret at column 5, got: %s", errorText)
	}
}

func TestParseErrorFormatWithFileName(t *testing.T) {
	parseError := &ParseError{
		File:       "web.ccattler",
		Line:       7,
		Col:        3,
		Message:    "expected identifier",
		SourceLine: "  123 bad",
	}

	errorText := parseError.Error()

	if !strings.HasPrefix(errorText, "web.ccattler:7:3:") {
		t.Errorf("expected file:line:col prefix, got: %s", errorText)
	}
}

func TestParseErrorFormatWithoutColumn(t *testing.T) {
	parseError := &ParseError{
		Line:       5,
		Message:    "service requires an image",
		SourceLine: "service web {",
	}

	errorText := parseError.Error()

	if !strings.HasPrefix(errorText, "5:") {
		t.Errorf("expected line-only prefix, got: %s", errorText)
	}
	if strings.Contains(errorText, "^") {
		t.Error("should not have caret when column is 0")
	}
}

func TestParseErrorFormatWithoutSourceLine(t *testing.T) {
	parseError := &ParseError{
		Line:    2,
		Col:     10,
		Message: "unexpected character",
	}

	errorText := parseError.Error()

	if strings.Count(errorText, "\n") > 0 {
		t.Error("should be single line without source context")
	}
}

func TestParseErrorIsUnwrappable(t *testing.T) {
	parseError := &ParseError{Line: 1, Col: 1, Message: "test"}
	var targetError *ParseError
	if !errors.As(parseError, &targetError) {
		t.Error("ParseError should be detectable via errors.As")
	}
}

func TestParseErrorFromParserIncludesSourceContext(t *testing.T) {
	input := "service web {\n    image nginx:1.28\n    bogus 42\n}"
	_, parseError := Parse(input)
	if parseError == nil {
		t.Fatal("expected error for unknown field")
	}

	var structuredError *ParseError
	if !errors.As(parseError, &structuredError) {
		t.Fatalf("expected *ParseError, got %T: %v", parseError, parseError)
	}

	if structuredError.Line != 3 {
		t.Errorf("line: got %d, want 3", structuredError.Line)
	}
	if structuredError.Col < 1 {
		t.Errorf("col should be > 0, got %d", structuredError.Col)
	}
	if !strings.Contains(structuredError.SourceLine, "bogus 42") {
		t.Errorf("source line should contain 'bogus 42', got %q", structuredError.SourceLine)
	}
	if !strings.Contains(structuredError.Message, "bogus") {
		t.Errorf("message should mention bogus, got: %s", structuredError.Message)
	}
}

func TestParseErrorFromLexerIncludesSourceContext(t *testing.T) {
	input := `service web {
    image "unterminated
}`
	_, lexerError := Parse(input)
	if lexerError == nil {
		t.Fatal("expected error for unterminated string")
	}

	var structuredError *ParseError
	if !errors.As(lexerError, &structuredError) {
		t.Fatalf("expected *ParseError, got %T: %v", lexerError, lexerError)
	}

	if structuredError.Line != 2 {
		t.Errorf("line: got %d, want 2", structuredError.Line)
	}
	if !strings.Contains(structuredError.Message, "unterminated string") {
		t.Errorf("message: got %q, want 'unterminated string'", structuredError.Message)
	}
	if structuredError.SourceLine == "" {
		t.Error("expected source line context")
	}
}

func TestParseErrorFromCompilerIncludesSourceContext(t *testing.T) {
	input := `service web {
    instances 3
}`
	file, parseError := Parse(input)
	if parseError != nil {
		t.Fatal(parseError)
	}

	sourceLines := splitSourceLines(input)
	_, compileError := CompileWithSource(file, sourceLines)
	if compileError == nil {
		t.Fatal("expected error for missing image")
	}

	var structuredError *ParseError
	if !errors.As(compileError, &structuredError) {
		t.Fatalf("expected *ParseError, got %T: %v", compileError, compileError)
	}

	if structuredError.Line != 1 {
		t.Errorf("line: got %d, want 1", structuredError.Line)
	}
	if !strings.Contains(structuredError.Message, "requires an image") {
		t.Errorf("message: got %q", structuredError.Message)
	}
	if structuredError.SourceLine != "service web {" {
		t.Errorf("source line: got %q, want %q", structuredError.SourceLine, "service web {")
	}
}

func TestParseErrorCaretAlignment(t *testing.T) {
	parseError := &ParseError{
		Line:       1,
		Col:        10,
		Message:    "test",
		SourceLine: "service { missing_name",
	}

	errorText := parseError.Error()
	lines := strings.Split(errorText, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), errorText)
	}

	caretLine := lines[2]
	expectedCaret := "  " + strings.Repeat(" ", 9) + "^"
	if caretLine != expectedCaret {
		t.Errorf("caret line: got %q, want %q", caretLine, expectedCaret)
	}
}

func TestParseWithFileNameIncludesFileInError(t *testing.T) {
	_, parseError := ParseWithFileName("bogus {}", "test.ccattler")
	if parseError == nil {
		t.Fatal("expected error")
	}

	var structuredError *ParseError
	if !errors.As(parseError, &structuredError) {
		t.Fatalf("expected *ParseError, got %T", parseError)
	}

	if structuredError.File != "test.ccattler" {
		t.Errorf("file: got %q, want %q", structuredError.File, "test.ccattler")
	}
	if !strings.HasPrefix(structuredError.Error(), "test.ccattler:") {
		t.Errorf("error should start with filename, got: %s", structuredError.Error())
	}
}
