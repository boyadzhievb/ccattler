package lang

import (
	"fmt"
	"strings"
	"unicode"
)

// Lexer tokenizes CCattler DSL source text into a stream of tokens.
type Lexer struct {
	input []rune // source text as a slice of Unicode code points
	pos   int    // current read position in the input slice
	line  int    // current line number (1-based) for error reporting
	col   int    // current column number (1-based) for error reporting
}

// NewLexer creates a new Lexer initialized to the beginning of the given input string.
func NewLexer(input string) *Lexer {
	return &Lexer{
		input: []rune(input),
		pos:   0,
		line:  1,
		col:   1,
	}
}

// Tokenize scans the entire input and returns a slice of all tokens, ending with TokenEOF.
func (l *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token
	for {
		token, err := l.scanNextToken()
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
		if token.Type == TokenEOF {
			break
		}
	}
	return tokens, nil
}

// scanNextToken reads the next token from the input, skipping whitespace and comments.
func (l *Lexer) scanNextToken() (Token, error) {
	l.skipWhitespaceExceptNewlines()
	l.skipLineComment()
	l.skipWhitespaceExceptNewlines()

	if l.pos >= len(l.input) {
		return Token{Type: TokenEOF, Line: l.line, Col: l.col}, nil
	}

	currentChar := l.input[l.pos]

	if currentChar == '\n' {
		token := Token{Type: TokenNewline, Value: "\n", Line: l.line, Col: l.col}
		l.advanceCursor()
		return token, nil
	}

	if currentChar == '{' {
		token := Token{Type: TokenLBrace, Value: "{", Line: l.line, Col: l.col}
		l.advanceCursor()
		return token, nil
	}
	if currentChar == '}' {
		token := Token{Type: TokenRBrace, Value: "}", Line: l.line, Col: l.col}
		l.advanceCursor()
		return token, nil
	}
	if currentChar == '/' {
		token := Token{Type: TokenSlash, Value: "/", Line: l.line, Col: l.col}
		l.advanceCursor()
		return token, nil
	}
	if currentChar == '=' {
		token := Token{Type: TokenEquals, Value: "=", Line: l.line, Col: l.col}
		l.advanceCursor()
		return token, nil
	}

	if currentChar == '"' {
		return l.scanQuotedString()
	}

	if unicode.IsDigit(currentChar) {
		return l.scanNumericLiteral(), nil
	}

	if isIdentifierStartChar(currentChar) {
		return l.scanIdentifier(), nil
	}

	return Token{}, fmt.Errorf("line %d col %d: unexpected character %q", l.line, l.col, currentChar)
}

// advanceCursor moves the lexer forward by one rune, updating line and column tracking.
func (l *Lexer) advanceCursor() {
	if l.pos < len(l.input) && l.input[l.pos] == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	l.pos++
}

// peekCurrentChar returns the current rune without advancing, or 0 if at end of input.
func (l *Lexer) peekCurrentChar() rune {
	if l.pos >= len(l.input) {
		return 0
	}
	return l.input[l.pos]
}

// skipWhitespaceExceptNewlines advances past spaces, tabs, and carriage returns, but stops at newlines.
func (l *Lexer) skipWhitespaceExceptNewlines() {
	for l.pos < len(l.input) && (l.input[l.pos] == ' ' || l.input[l.pos] == '\t' || l.input[l.pos] == '\r') {
		l.advanceCursor()
	}
}

// skipLineComment advances past a '#'-initiated comment up to (but not including) the newline.
func (l *Lexer) skipLineComment() {
	if l.pos < len(l.input) && l.input[l.pos] == '#' {
		for l.pos < len(l.input) && l.input[l.pos] != '\n' {
			l.advanceCursor()
		}
	}
}

// scanQuotedString reads a double-quoted string literal and returns a TokenString token.
func (l *Lexer) scanQuotedString() (Token, error) {
	startLine, startCol := l.line, l.col
	l.advanceCursor() // skip opening quote
	var builder strings.Builder
	for l.pos < len(l.input) && l.input[l.pos] != '"' {
		if l.input[l.pos] == '\n' {
			return Token{}, fmt.Errorf("line %d col %d: unterminated string", startLine, startCol)
		}
		builder.WriteRune(l.input[l.pos])
		l.advanceCursor()
	}
	if l.pos >= len(l.input) {
		return Token{}, fmt.Errorf("line %d col %d: unterminated string", startLine, startCol)
	}
	l.advanceCursor() // skip closing quote
	return Token{Type: TokenString, Value: builder.String(), Line: startLine, Col: startCol}, nil
}

// scanNumericLiteral reads a number (with optional decimal point, percent, and unit suffix) and returns a TokenNumber token.
func (l *Lexer) scanNumericLiteral() Token {
	startCol := l.col
	var builder strings.Builder
	for l.pos < len(l.input) && isNumericChar(l.input[l.pos]) {
		builder.WriteRune(l.input[l.pos])
		l.advanceCursor()
	}
	// Allow unit suffixes: 500m, 512Mi, 100Gi, 10s, etc.
	for l.pos < len(l.input) && unicode.IsLetter(l.input[l.pos]) {
		builder.WriteRune(l.input[l.pos])
		l.advanceCursor()
	}
	return Token{Type: TokenNumber, Value: builder.String(), Line: l.line, Col: startCol}
}

// scanIdentifier reads an identifier (keyword or name) and returns a TokenIdent token.
func (l *Lexer) scanIdentifier() Token {
	startCol := l.col
	var builder strings.Builder
	for l.pos < len(l.input) && isIdentifierContinuationChar(l.input[l.pos]) {
		builder.WriteRune(l.input[l.pos])
		l.advanceCursor()
	}
	return Token{Type: TokenIdent, Value: builder.String(), Line: l.line, Col: startCol}
}

// isIdentifierStartChar reports whether the rune can begin an identifier (letter or underscore).
func isIdentifierStartChar(currentChar rune) bool {
	return unicode.IsLetter(currentChar) || currentChar == '_'
}

// isIdentifierContinuationChar reports whether the rune can continue an identifier (letter, digit, underscore, hyphen, dot, or colon).
func isIdentifierContinuationChar(currentChar rune) bool {
	return unicode.IsLetter(currentChar) || unicode.IsDigit(currentChar) || currentChar == '_' || currentChar == '-' || currentChar == '.' || currentChar == ':'
}

// isNumericChar reports whether the rune is part of a numeric literal (digit, dot, or percent).
func isNumericChar(currentChar rune) bool {
	return unicode.IsDigit(currentChar) || currentChar == '.' || currentChar == '%'
}
