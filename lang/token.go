package lang

// TokenType represents the category of a lexical token produced by the CCattler DSL lexer.
type TokenType int

const (
	// TokenEOF signals the end of the input stream.
	TokenEOF TokenType = iota
	// TokenIdent represents an identifier such as a keyword, service name, or field name.
	TokenIdent
	// TokenNumber represents a numeric literal (integer or decimal) in the DSL source.
	TokenNumber
	// TokenString represents a quoted string literal.
	TokenString
	// TokenLBrace represents a left curly brace '{', which opens a block.
	TokenLBrace
	// TokenRBrace represents a right curly brace '}', which closes a block.
	TokenRBrace
	// TokenSlash represents a forward slash '/', used in path expressions and network rules.
	TokenSlash
	// TokenEquals represents an equals sign '=', used in assignments and comparisons.
	TokenEquals
	// TokenNewline represents a newline character that terminates a statement.
	TokenNewline
)

// Token is a single lexical unit produced by scanning CCattler DSL source text.
type Token struct {
	Type  TokenType // Type is the category of this token (identifier, number, brace, etc.).
	Value string    // Value is the raw text that was scanned to produce this token.
	Line  int       // Line is the 1-based line number where this token begins.
	Col   int       // Col is the 1-based column number where this token begins.
}

// String returns a human-readable name for the token type, useful for error messages and debugging.
func (t TokenType) String() string {
	switch t {
	case TokenEOF:
		return "EOF"
	case TokenIdent:
		return "Ident"
	case TokenNumber:
		return "Number"
	case TokenString:
		return "String"
	case TokenLBrace:
		return "LBrace"
	case TokenRBrace:
		return "RBrace"
	case TokenSlash:
		return "Slash"
	case TokenEquals:
		return "Equals"
	case TokenNewline:
		return "Newline"
	default:
		return "Unknown"
	}
}
