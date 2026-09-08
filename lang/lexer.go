package lang

import (
	"fmt"
	"strings"
	"unicode"
)

// Lexer tokenizes CCattler DSL source text into a stream of tokens.
type Lexer struct {
	input    []rune // source text as a slice of Unicode code points
	position int    // current read position in the input slice
	line     int    // current line number (1-based) for error reporting
	column   int    // current column number (1-based) for error reporting
}

// NewLexer creates a new Lexer initialized to the beginning of the given input string.
func NewLexer(input string) *Lexer {
	return &Lexer{
		input:    []rune(input),
		position: 0,
		line:     1,
		column:   1,
	}
}

// Tokenize scans the entire input and returns a slice of all tokens, ending with TokenEOF.
func (lexer *Lexer) Tokenize() ([]Token, error) {
	var tokens []Token
	for {
		token, err := lexer.scanNextToken()
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
func (lexer *Lexer) scanNextToken() (Token, error) {
	lexer.skipWhitespaceExceptNewlines()
	lexer.skipLineComment()
	lexer.skipWhitespaceExceptNewlines()

	if lexer.position >= len(lexer.input) {
		return Token{Type: TokenEOF, Line: lexer.line, Col: lexer.column}, nil
	}

	currentChar := lexer.input[lexer.position]

	if currentChar == '\n' {
		token := Token{Type: TokenNewline, Value: "\n", Line: lexer.line, Col: lexer.column}
		lexer.advanceCursor()
		return token, nil
	}

	if currentChar == '{' {
		token := Token{Type: TokenLBrace, Value: "{", Line: lexer.line, Col: lexer.column}
		lexer.advanceCursor()
		return token, nil
	}
	if currentChar == '}' {
		token := Token{Type: TokenRBrace, Value: "}", Line: lexer.line, Col: lexer.column}
		lexer.advanceCursor()
		return token, nil
	}
	if currentChar == '/' {
		token := Token{Type: TokenSlash, Value: "/", Line: lexer.line, Col: lexer.column}
		lexer.advanceCursor()
		return token, nil
	}
	if currentChar == '=' {
		token := Token{Type: TokenEquals, Value: "=", Line: lexer.line, Col: lexer.column}
		lexer.advanceCursor()
		return token, nil
	}

	if currentChar == '"' {
		return lexer.scanQuotedString()
	}

	if unicode.IsDigit(currentChar) {
		return lexer.scanNumericLiteral(), nil
	}

	if isIdentifierStartChar(currentChar) {
		return lexer.scanIdentifier(), nil
	}

	return Token{}, fmt.Errorf("line %d col %d: unexpected character %q", lexer.line, lexer.column, currentChar)
}

// advanceCursor moves the lexer forward by one rune, updating line and column tracking.
func (lexer *Lexer) advanceCursor() {
	if lexer.position < len(lexer.input) && lexer.input[lexer.position] == '\n' {
		lexer.line++
		lexer.column = 1
	} else {
		lexer.column++
	}
	lexer.position++
}

// peekCurrentChar returns the current rune without advancing, or 0 if at end of input.
func (lexer *Lexer) peekCurrentChar() rune {
	if lexer.position >= len(lexer.input) {
		return 0
	}
	return lexer.input[lexer.position]
}

// skipWhitespaceExceptNewlines advances past spaces, tabs, and carriage returns, but stops at newlines.
func (lexer *Lexer) skipWhitespaceExceptNewlines() {
	for lexer.position < len(lexer.input) && (lexer.input[lexer.position] == ' ' || lexer.input[lexer.position] == '\t' || lexer.input[lexer.position] == '\r') {
		lexer.advanceCursor()
	}
}

// skipLineComment advances past a '#'-initiated comment up to (but not including) the newline.
func (lexer *Lexer) skipLineComment() {
	if lexer.position < len(lexer.input) && lexer.input[lexer.position] == '#' {
		for lexer.position < len(lexer.input) && lexer.input[lexer.position] != '\n' {
			lexer.advanceCursor()
		}
	}
}

// scanQuotedString reads a double-quoted string literal and returns a TokenString token.
func (lexer *Lexer) scanQuotedString() (Token, error) {
	startLine, startCol := lexer.line, lexer.column
	lexer.advanceCursor() // skip opening quote
	var builder strings.Builder
	for lexer.position < len(lexer.input) && lexer.input[lexer.position] != '"' {
		if lexer.input[lexer.position] == '\n' {
			return Token{}, fmt.Errorf("line %d col %d: unterminated string", startLine, startCol)
		}
		builder.WriteRune(lexer.input[lexer.position])
		lexer.advanceCursor()
	}
	if lexer.position >= len(lexer.input) {
		return Token{}, fmt.Errorf("line %d col %d: unterminated string", startLine, startCol)
	}
	lexer.advanceCursor() // skip closing quote
	return Token{Type: TokenString, Value: builder.String(), Line: startLine, Col: startCol}, nil
}

// scanNumericLiteral reads a number (with optional decimal point, percent, and unit suffix) and returns a TokenNumber token.
func (lexer *Lexer) scanNumericLiteral() Token {
	startCol := lexer.column
	var builder strings.Builder
	for lexer.position < len(lexer.input) && isNumericChar(lexer.input[lexer.position]) {
		builder.WriteRune(lexer.input[lexer.position])
		lexer.advanceCursor()
	}
	// Allow unit suffixes: 500m, 512Mi, 100Gi, 10s, etc.
	for lexer.position < len(lexer.input) && unicode.IsLetter(lexer.input[lexer.position]) {
		builder.WriteRune(lexer.input[lexer.position])
		lexer.advanceCursor()
	}
	return Token{Type: TokenNumber, Value: builder.String(), Line: lexer.line, Col: startCol}
}

// scanIdentifier reads an identifier (keyword or name) and returns a TokenIdent token.
func (lexer *Lexer) scanIdentifier() Token {
	startCol := lexer.column
	var builder strings.Builder
	for lexer.position < len(lexer.input) && isIdentifierContinuationChar(lexer.input[lexer.position]) {
		builder.WriteRune(lexer.input[lexer.position])
		lexer.advanceCursor()
	}
	return Token{Type: TokenIdent, Value: builder.String(), Line: lexer.line, Col: startCol}
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
