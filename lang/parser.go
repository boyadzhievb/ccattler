package lang

import (
	"fmt"
	"strconv"
)

// Parser is a recursive-descent parser that transforms a flat token stream
// into an AST representing a CCattler configuration file.
type Parser struct {
	tokens []Token // tokens is the complete list of tokens produced by the lexer.
	pos    int     // pos is the current read position within the token slice.
}

// NewParser creates a Parser from a pre-lexed token slice.
func NewParser(tokens []Token) *Parser {
	return &Parser{tokens: tokens}
}

// Parse lexes the input string and parses the resulting tokens into a File AST.
func Parse(input string) (*File, error) {
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		return nil, err
	}
	return NewParser(tokens).ParseFile()
}

// ParseFile parses the top-level declarations of a CCattler file and returns
// the complete File AST.
func (p *Parser) ParseFile() (*File, error) {
	file := &File{}
	p.skipNewlineTokens()

	for !p.isAtEnd() {
		token := p.currentToken()
		if token.Type != TokenIdent {
			return nil, p.parserErrorf("expected declaration keyword, got %s %q", token.Type, token.Value)
		}

		switch token.Value {
		case "service":
			serviceDecl, err := p.parseServiceDeclaration()
			if err != nil {
				return nil, err
			}
			file.Services = append(file.Services, *serviceDecl)
		default:
			return nil, p.parserErrorf("unknown declaration %q", token.Value)
		}

		p.skipNewlineTokens()
	}

	return file, nil
}

// parseServiceDeclaration parses a service block, including its name and all
// nested fields (image, instances, expose, resources, health).
func (p *Parser) parseServiceDeclaration() (*ServiceDecl, error) {
	line := p.currentToken().Line
	p.advanceToken() // skip "service"

	name, err := p.expectIdentifier()
	if err != nil {
		return nil, err
	}

	if err := p.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	p.skipNewlineTokens()

	serviceDecl := &ServiceDecl{Name: name, Line: line}

	for !p.currentTokenIs(TokenRBrace) && !p.isAtEnd() {
		key, err := p.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "image":
			serviceDecl.Image, err = p.expectStringOrIdentifier()
		case "instances":
			serviceDecl.Instances, err = p.expectInteger()
		case "expose":
			var port int
			port, err = p.expectInteger()
			if err == nil {
				serviceDecl.Ports = append(serviceDecl.Ports, port)
			}
		case "resources":
			serviceDecl.Resources, err = p.parseResourcesBlock()
		case "health":
			serviceDecl.Health, err = p.parseHealthBlock()
		default:
			return nil, p.parserErrorf("unknown service field %q", key)
		}
		if err != nil {
			return nil, err
		}

		p.skipNewlineTokens()
	}

	if err := p.expectToken(TokenRBrace); err != nil {
		return nil, err
	}

	return serviceDecl, nil
}

// parseResourcesBlock parses a resources { ... } block containing cpu and
// memory declarations.
func (p *Parser) parseResourcesBlock() (*ResourcesDecl, error) {
	if err := p.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	p.skipNewlineTokens()

	resourcesDecl := &ResourcesDecl{}
	for !p.currentTokenIs(TokenRBrace) && !p.isAtEnd() {
		key, err := p.expectIdentifier()
		if err != nil {
			return nil, err
		}

		valueToken := p.currentToken()
		if valueToken.Type != TokenNumber && valueToken.Type != TokenIdent {
			return nil, p.parserErrorf("expected value for %s, got %s", key, valueToken.Type)
		}
		p.advanceToken()

		switch key {
		case "cpu":
			resourcesDecl.CPU = valueToken.Value
		case "memory":
			resourcesDecl.Memory = valueToken.Value
		default:
			return nil, p.parserErrorf("unknown resource field %q", key)
		}

		p.skipNewlineTokens()
	}

	if err := p.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return resourcesDecl, nil
}

// parseHealthBlock parses a health { ... } block containing the health check
// method (http or tcp), optional path, and interval.
func (p *Parser) parseHealthBlock() (*HealthDecl, error) {
	if err := p.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	p.skipNewlineTokens()

	healthDecl := &HealthDecl{}
	for !p.currentTokenIs(TokenRBrace) && !p.isAtEnd() {
		key, err := p.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "http":
			healthDecl.Method = "http"
			if p.currentTokenIs(TokenSlash) {
				p.advanceToken()
				path, err := p.expectIdentifier()
				if err != nil {
					return nil, err
				}
				healthDecl.Path = "/" + path
			}
		case "tcp":
			healthDecl.Method = "tcp"
		case "every":
			token := p.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, p.parserErrorf("expected interval value, got %s", token.Type)
			}
			healthDecl.Interval = token.Value
			p.advanceToken()
		default:
			return nil, p.parserErrorf("unknown health field %q", key)
		}

		p.skipNewlineTokens()
	}

	if err := p.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return healthDecl, nil
}

// Helpers

// currentToken returns the token at the current position, or a synthetic EOF
// token if the parser has consumed all input.
func (p *Parser) currentToken() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokenEOF}
	}
	return p.tokens[p.pos]
}

// advanceToken moves the parser position forward by one token.
func (p *Parser) advanceToken() {
	p.pos++
}

// isAtEnd reports whether the parser has reached the end of the token stream.
func (p *Parser) isAtEnd() bool {
	return p.pos >= len(p.tokens) || p.tokens[p.pos].Type == TokenEOF
}

// currentTokenIs reports whether the current token has the given type.
func (p *Parser) currentTokenIs(tokenType TokenType) bool {
	return p.currentToken().Type == tokenType
}

// expectToken consumes the current token if it matches the given type, or
// returns a descriptive parse error.
func (p *Parser) expectToken(tokenType TokenType) error {
	if p.currentToken().Type != tokenType {
		return p.parserErrorf("expected %s, got %s %q", tokenType, p.currentToken().Type, p.currentToken().Value)
	}
	p.advanceToken()
	return nil
}

// expectIdentifier consumes and returns the current token's value if it is an
// identifier, or returns a parse error.
func (p *Parser) expectIdentifier() (string, error) {
	token := p.currentToken()
	if token.Type != TokenIdent {
		return "", p.parserErrorf("expected identifier, got %s %q", token.Type, token.Value)
	}
	p.advanceToken()
	return token.Value, nil
}

// expectInteger consumes and returns the current token's value as an int if it
// is a number literal, or returns a parse error.
func (p *Parser) expectInteger() (int, error) {
	token := p.currentToken()
	if token.Type != TokenNumber {
		return 0, p.parserErrorf("expected number, got %s %q", token.Type, token.Value)
	}
	p.advanceToken()
	n, err := strconv.Atoi(token.Value)
	if err != nil {
		return 0, p.parserErrorf("invalid integer %q", token.Value)
	}
	return n, nil
}

// skipNewlineTokens advances past any consecutive newline tokens.
func (p *Parser) skipNewlineTokens() {
	for p.currentToken().Type == TokenNewline {
		p.advanceToken()
	}
}

// expectStringOrIdentifier consumes and returns the current token's value if
// it is either a quoted string or an identifier.
func (p *Parser) expectStringOrIdentifier() (string, error) {
	token := p.currentToken()
	if token.Type == TokenString {
		p.advanceToken()
		return token.Value, nil
	}
	return p.expectIdentifier()
}

// parserErrorf returns a formatted error that includes the current token's
// line and column for diagnostic context.
func (p *Parser) parserErrorf(format string, args ...any) error {
	token := p.currentToken()
	return fmt.Errorf("line %d col %d: %s", token.Line, token.Col, fmt.Sprintf(format, args...))
}
