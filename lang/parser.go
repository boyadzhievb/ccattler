package lang

import (
	"fmt"
	"strconv"
)

// Parser is a recursive-descent parser that transforms a flat token stream
// into an AST representing a CCattler configuration file.
type Parser struct {
	tokens   []Token // tokens is the complete list of tokens produced by the lexer.
	position int     // position is the current read position within the token slice.
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
func (parser *Parser) ParseFile() (*File, error) {
	file := &File{}
	parser.skipNewlineTokens()

	for !parser.isAtEnd() {
		token := parser.currentToken()
		if token.Type != TokenIdent {
			return nil, parser.parserErrorf("expected declaration keyword, got %s %q", token.Type, token.Value)
		}

		switch token.Value {
		case "service":
			serviceDecl, err := parser.parseServiceDeclaration()
			if err != nil {
				return nil, err
			}
			file.Services = append(file.Services, *serviceDecl)
		case "volume":
			volumeDecl, err := parser.parseVolumeDeclaration()
			if err != nil {
				return nil, err
			}
			file.Volumes = append(file.Volumes, *volumeDecl)
		case "tenant":
			tenantDecl, err := parser.parseTenantDeclaration()
			if err != nil {
				return nil, err
			}
			file.Tenants = append(file.Tenants, *tenantDecl)
		default:
			return nil, parser.parserErrorf("unknown declaration %q", token.Value)
		}

		parser.skipNewlineTokens()
	}

	return file, nil
}

// parseServiceDeclaration parses a service block, including its name and all
// nested fields (image, instances, expose, resources, health).
func (parser *Parser) parseServiceDeclaration() (*ServiceDecl, error) {
	line := parser.currentToken().Line
	parser.advanceToken() // skip "service"

	name, err := parser.expectHierarchicalName()
	if err != nil {
		return nil, err
	}

	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	serviceDecl := &ServiceDecl{Name: name, Line: line}

	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "image":
			serviceDecl.Image, err = parser.expectStringOrIdentifier()
		case "instances":
			serviceDecl.Instances, err = parser.expectInteger()
		case "expose":
			var port int
			port, err = parser.expectInteger()
			if err == nil {
				serviceDecl.Ports = append(serviceDecl.Ports, port)
			}
		case "resources":
			serviceDecl.Resources, err = parser.parseResourcesBlock()
		case "health":
			serviceDecl.Health, err = parser.parseHealthBlock()
		case "scale":
			serviceDecl.Scale, err = parser.parseScaleBlock()
		case "placement":
			serviceDecl.Placement, err = parser.parsePlacementBlock()
		case "update":
			serviceDecl.Update, err = parser.parseUpdateBlock()
		case "owner":
			serviceDecl.Owner, err = parser.expectIdentifier()
		case "config":
			serviceDecl.Config, err = parser.parseConfigBlock()
		case "secret":
			var secretDecl SecretDecl
			secretDecl, err = parser.parseSecretDeclaration()
			if err == nil {
				serviceDecl.Secrets = append(serviceDecl.Secrets, secretDecl)
			}
		case "startup":
			serviceDecl.Startup, err = parser.parseProbeBlock()
		case "liveness":
			serviceDecl.Liveness, err = parser.parseProbeBlock()
		case "readiness":
			serviceDecl.Readiness, err = parser.parseProbeBlock()
		case "init":
			var initStepDecl InitStepDecl
			initStepDecl, err = parser.parseInitStepBlock()
			if err == nil {
				serviceDecl.InitSteps = append(serviceDecl.InitSteps, initStepDecl)
			}
		case "volume":
			volumeName, mountErr := parser.expectIdentifier()
			if mountErr != nil {
				return nil, mountErr
			}
			mountPath, mountErr := parser.readMountPath()
			if mountErr != nil {
				return nil, mountErr
			}
			serviceDecl.VolumeMounts = append(serviceDecl.VolumeMounts, VolumeMountDecl{
				VolumeName: volumeName,
				MountPath:  mountPath,
			})
		default:
			return nil, parser.parserErrorf("unknown service field %q", key)
		}
		if err != nil {
			return nil, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}

	return serviceDecl, nil
}

// parseResourcesBlock parses a resources { ... } block containing cpu and
// memory declarations.
func (parser *Parser) parseResourcesBlock() (*ResourcesDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	resourcesDecl := &ResourcesDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		valueToken := parser.currentToken()
		if valueToken.Type != TokenNumber && valueToken.Type != TokenIdent {
			return nil, parser.parserErrorf("expected value for %s, got %s", key, valueToken.Type)
		}
		parser.advanceToken()

		switch key {
		case "cpu":
			resourcesDecl.CPU = valueToken.Value
		case "memory":
			resourcesDecl.Memory = valueToken.Value
		default:
			return nil, parser.parserErrorf("unknown resource field %q", key)
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return resourcesDecl, nil
}

// parseHealthBlock parses a health { ... } block containing the health check
// method (http or tcp), optional path, and interval.
func (parser *Parser) parseHealthBlock() (*HealthDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	healthDecl := &HealthDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "http":
			healthDecl.Method = "http"
			if parser.currentTokenIs(TokenSlash) {
				parser.advanceToken()
				path, err := parser.expectIdentifier()
				if err != nil {
					return nil, err
				}
				healthDecl.Path = "/" + path
			}
		case "tcp":
			healthDecl.Method = "tcp"
		case "every":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected interval value, got %s", token.Type)
			}
			healthDecl.Interval = token.Value
			parser.advanceToken()
		default:
			return nil, parser.parserErrorf("unknown health field %q", key)
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return healthDecl, nil
}

// parseProbeBlock parses a startup/liveness/readiness probe block. Supported
// fields: http, tcp, exec (method), port, every (interval), timeout,
// failure_threshold, success_threshold, initial_delay.
func (parser *Parser) parseProbeBlock() (*ProbeDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	probeDecl := &ProbeDecl{FailureThreshold: 3, SuccessThreshold: 1}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "http":
			probeDecl.Method = "http"
			if parser.currentTokenIs(TokenSlash) {
				parser.advanceToken()
				path, pathErr := parser.expectIdentifier()
				if pathErr != nil {
					return nil, pathErr
				}
				probeDecl.Path = "/" + path
			}
		case "tcp":
			probeDecl.Method = "tcp"
		case "exec":
			probeDecl.Method = "exec"
			// exec command follows as a string
			if parser.currentTokenIs(TokenString) || parser.currentTokenIs(TokenIdent) {
				probeDecl.Path = parser.currentToken().Value
				parser.advanceToken()
			}
		case "port":
			probeDecl.Port, err = parser.expectInteger()
		case "every":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected interval value, got %s", token.Type)
			}
			probeDecl.Interval = token.Value
			parser.advanceToken()
		case "timeout":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected timeout value, got %s", token.Type)
			}
			probeDecl.Timeout = token.Value
			parser.advanceToken()
		case "failure_threshold":
			probeDecl.FailureThreshold, err = parser.expectInteger()
		case "success_threshold":
			probeDecl.SuccessThreshold, err = parser.expectInteger()
		case "initial_delay":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected duration for initial_delay, got %s", token.Type)
			}
			probeDecl.InitialDelay = token.Value
			parser.advanceToken()
		default:
			return nil, parser.parserErrorf("unknown probe field %q", key)
		}
		if err != nil {
			return nil, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}

	if probeDecl.Method == "" {
		return nil, parser.parserErrorf("probe block requires a method (http, tcp, or exec)")
	}

	return probeDecl, nil
}

// parseScaleBlock parses a scale { ... } block containing a horizontal sub-block.
func (parser *Parser) parseScaleBlock() (*ScaleDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	scaleDecl := &ScaleDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "horizontal":
			scaleDecl.Horizontal, err = parser.parseHorizontalScaleBlock()
		case "vertical":
			scaleDecl.Vertical, err = parser.parseVerticalScaleBlock()
		default:
			return nil, parser.parserErrorf("unknown scale field %q", key)
		}
		if err != nil {
			return nil, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return scaleDecl, nil
}

// parseHorizontalScaleBlock parses a horizontal { min N, max M, target X = Y } block.
func (parser *Parser) parseHorizontalScaleBlock() (*HorizontalScaleDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	horizontalDecl := &HorizontalScaleDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "min":
			horizontalDecl.Min, err = parser.expectInteger()
		case "max":
			horizontalDecl.Max, err = parser.expectInteger()
		case "target":
			metricName, targetErr := parser.expectIdentifier()
			if targetErr != nil {
				return nil, targetErr
			}
			if targetErr = parser.expectToken(TokenEquals); targetErr != nil {
				return nil, targetErr
			}
			targetToken := parser.currentToken()
			if targetToken.Type != TokenNumber {
				return nil, parser.parserErrorf("expected number for target value, got %s %q", targetToken.Type, targetToken.Value)
			}
			parser.advanceToken()
			targetValue, targetErr := parseTargetValue(targetToken.Value)
			if targetErr != nil {
				return nil, parser.parserErrorf("invalid target value %q: %v", targetToken.Value, targetErr)
			}
			horizontalDecl.Targets = append(horizontalDecl.Targets, ScaleTargetDecl{
				Metric: metricName,
				Value:  targetValue,
			})
		case "event":
			eventDecl, eventErr := parser.parseEventScaleEntry()
			if eventErr != nil {
				return nil, eventErr
			}
			horizontalDecl.Events = append(horizontalDecl.Events, *eventDecl)
		case "schedule":
			horizontalDecl.Schedule, err = parser.parseScheduleBlock()
		case "stabilization":
			horizontalDecl.Stabilization, err = parser.parseStabilizationBlock()
		default:
			return nil, parser.parserErrorf("unknown horizontal scale field %q", key)
		}
		if err != nil {
			return nil, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return horizontalDecl, nil
}

// parseEventScaleEntry parses "event source_name = target_value" inside a horizontal block.
func (parser *Parser) parseEventScaleEntry() (*EventScaleDecl, error) {
	sourceName, err := parser.expectIdentifier()
	if err != nil {
		return nil, err
	}
	if err := parser.expectToken(TokenEquals); err != nil {
		return nil, err
	}
	targetValue, err := parser.expectInteger()
	if err != nil {
		return nil, err
	}
	return &EventScaleDecl{Source: sourceName, Target: targetValue}, nil
}

// parseScheduleBlock parses a schedule { days ..., start ..., end ..., minimum N } block.
func (parser *Parser) parseScheduleBlock() (*ScheduleDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	scheduleDecl := &ScheduleDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "days":
			scheduleDecl.Days, err = parser.expectIdentifier()
		case "start":
			scheduleDecl.Start, err = parser.expectStringOrIdentifier()
		case "end":
			scheduleDecl.End, err = parser.expectStringOrIdentifier()
		case "minimum":
			scheduleDecl.Minimum, err = parser.expectInteger()
		default:
			return nil, parser.parserErrorf("unknown schedule field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return scheduleDecl, nil
}

// parseStabilizationBlock parses a stabilization { scale_up 60s, scale_down 300s } block.
func (parser *Parser) parseStabilizationBlock() (*StabilizationDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	stabilizationDecl := &StabilizationDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "scale_up":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected duration for scale_up, got %s", token.Type)
			}
			stabilizationDecl.ScaleUp = token.Value
			parser.advanceToken()
		case "scale_down":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return nil, parser.parserErrorf("expected duration for scale_down, got %s", token.Type)
			}
			stabilizationDecl.ScaleDown = token.Value
			parser.advanceToken()
		default:
			return nil, parser.parserErrorf("unknown stabilization field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return stabilizationDecl, nil
}

// parseVerticalScaleBlock parses a vertical { cpu { min X, max Y }, memory { min X, max Y } } block.
func (parser *Parser) parseVerticalScaleBlock() (*VerticalScaleDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	verticalDecl := &VerticalScaleDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "cpu":
			if err := parser.expectToken(TokenLBrace); err != nil {
				return nil, err
			}
			parser.skipNewlineTokens()
			for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
				subKey, subErr := parser.expectIdentifier()
				if subErr != nil {
					return nil, subErr
				}
				valueToken := parser.currentToken()
				if valueToken.Type != TokenNumber && valueToken.Type != TokenIdent {
					return nil, parser.parserErrorf("expected value for cpu %s, got %s", subKey, valueToken.Type)
				}
				parser.advanceToken()
				switch subKey {
				case "min":
					verticalDecl.CPUMin = valueToken.Value
				case "max":
					verticalDecl.CPUMax = valueToken.Value
				default:
					return nil, parser.parserErrorf("unknown vertical cpu field %q", subKey)
				}
				parser.skipNewlineTokens()
			}
			if err := parser.expectToken(TokenRBrace); err != nil {
				return nil, err
			}
		case "memory":
			if err := parser.expectToken(TokenLBrace); err != nil {
				return nil, err
			}
			parser.skipNewlineTokens()
			for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
				subKey, subErr := parser.expectIdentifier()
				if subErr != nil {
					return nil, subErr
				}
				valueToken := parser.currentToken()
				if valueToken.Type != TokenNumber && valueToken.Type != TokenIdent {
					return nil, parser.parserErrorf("expected value for memory %s, got %s", subKey, valueToken.Type)
				}
				parser.advanceToken()
				switch subKey {
				case "min":
					verticalDecl.MemoryMin = valueToken.Value
				case "max":
					verticalDecl.MemoryMax = valueToken.Value
				default:
					return nil, parser.parserErrorf("unknown vertical memory field %q", subKey)
				}
				parser.skipNewlineTokens()
			}
			if err := parser.expectToken(TokenRBrace); err != nil {
				return nil, err
			}
		default:
			return nil, parser.parserErrorf("unknown vertical scale field %q", key)
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return verticalDecl, nil
}

// parsePlacementBlock parses a placement { architecture ..., zone ... } block.
func (parser *Parser) parsePlacementBlock() (*PlacementDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	placementDecl := &PlacementDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "architecture":
			placementDecl.Architecture, err = parser.expectIdentifier()
		case "zone":
			placementDecl.ZonePolicy, err = parser.expectIdentifier()
		default:
			return nil, parser.parserErrorf("unknown placement field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return placementDecl, nil
}

// parseUpdateBlock parses an update { max_unavailable N, max_extra M } block.
func (parser *Parser) parseUpdateBlock() (*UpdateDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	updateDecl := &UpdateDecl{MaxUnavailable: 1, MaxExtra: 1}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "max_unavailable":
			updateDecl.MaxUnavailable, err = parser.expectInteger()
		case "max_extra":
			updateDecl.MaxExtra, err = parser.expectInteger()
		default:
			return nil, parser.parserErrorf("unknown update field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return updateDecl, nil
}

// parseTargetValue extracts an integer from a token value that may include a
// trailing percent sign (e.g. "60%" -> 60, "500" -> 500).
func parseTargetValue(raw string) (int, error) {
	cleaned := raw
	if len(cleaned) > 0 && cleaned[len(cleaned)-1] == '%' {
		cleaned = cleaned[:len(cleaned)-1]
	}
	return strconv.Atoi(cleaned)
}

// parseVolumeDeclaration parses a volume block, including its name and all
// nested fields (size, persistent).
func (parser *Parser) parseVolumeDeclaration() (*VolumeDecl, error) {
	line := parser.currentToken().Line
	parser.advanceToken() // skip "volume"

	name, err := parser.expectIdentifier()
	if err != nil {
		return nil, err
	}

	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	volumeDecl := &VolumeDecl{Name: name, Line: line}

	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "size":
			sizeToken := parser.currentToken()
			if sizeToken.Type != TokenNumber && sizeToken.Type != TokenIdent && sizeToken.Type != TokenString {
				return nil, parser.parserErrorf("expected size value, got %s %q", sizeToken.Type, sizeToken.Value)
			}
			volumeDecl.Size = sizeToken.Value
			parser.advanceToken()
		case "persistent":
			persistentValue, persistentErr := parser.expectIdentifier()
			if persistentErr != nil {
				return nil, persistentErr
			}
			volumeDecl.Persistent = persistentValue == "true"
		default:
			return nil, parser.parserErrorf("unknown volume field %q", key)
		}
		if err != nil {
			return nil, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}

	return volumeDecl, nil
}

// readMountPath reads a filesystem mount path, which can be either a quoted
// string ("/var/lib/data") or a bare slash-separated path (/var/lib/data).
func (parser *Parser) readMountPath() (string, error) {
	if parser.currentTokenIs(TokenString) {
		value := parser.currentToken().Value
		parser.advanceToken()
		return value, nil
	}

	if !parser.currentTokenIs(TokenSlash) {
		return "", parser.parserErrorf("expected mount path (quoted string or /path), got %s %q",
			parser.currentToken().Type, parser.currentToken().Value)
	}

	pathValue := ""
	for parser.currentTokenIs(TokenSlash) {
		pathValue += "/"
		parser.advanceToken()
		if parser.currentTokenIs(TokenIdent) || parser.currentTokenIs(TokenNumber) {
			pathValue += parser.currentToken().Value
			parser.advanceToken()
		}
	}
	return pathValue, nil
}

// Helpers

// currentToken returns the token at the current position, or a synthetic EOF
// token if the parser has consumed all input.
func (parser *Parser) currentToken() Token {
	if parser.position >= len(parser.tokens) {
		return Token{Type: TokenEOF}
	}
	return parser.tokens[parser.position]
}

// advanceToken moves the parser position forward by one token.
func (parser *Parser) advanceToken() {
	parser.position++
}

// isAtEnd reports whether the parser has reached the end of the token stream.
func (parser *Parser) isAtEnd() bool {
	return parser.position >= len(parser.tokens) || parser.tokens[parser.position].Type == TokenEOF
}

// currentTokenIs reports whether the current token has the given type.
func (parser *Parser) currentTokenIs(tokenType TokenType) bool {
	return parser.currentToken().Type == tokenType
}

// expectToken consumes the current token if it matches the given type, or
// returns a descriptive parse error.
func (parser *Parser) expectToken(tokenType TokenType) error {
	if parser.currentToken().Type != tokenType {
		return parser.parserErrorf("expected %s, got %s %q", tokenType, parser.currentToken().Type, parser.currentToken().Value)
	}
	parser.advanceToken()
	return nil
}

// expectIdentifier consumes and returns the current token's value if it is an
// identifier, or returns a parse error.
func (parser *Parser) expectIdentifier() (string, error) {
	token := parser.currentToken()
	if token.Type != TokenIdent {
		return "", parser.parserErrorf("expected identifier, got %s %q", token.Type, token.Value)
	}
	parser.advanceToken()
	return token.Value, nil
}

// expectInteger consumes and returns the current token's value as an int if it
// is a number literal, or returns a parse error.
func (parser *Parser) expectInteger() (int, error) {
	token := parser.currentToken()
	if token.Type != TokenNumber {
		return 0, parser.parserErrorf("expected number, got %s %q", token.Type, token.Value)
	}
	parser.advanceToken()
	parsedValue, err := strconv.Atoi(token.Value)
	if err != nil {
		return 0, parser.parserErrorf("invalid integer %q", token.Value)
	}
	return parsedValue, nil
}

// skipNewlineTokens advances past any consecutive newline tokens.
func (parser *Parser) skipNewlineTokens() {
	for parser.currentToken().Type == TokenNewline {
		parser.advanceToken()
	}
}

// expectHierarchicalName parses an identifier optionally followed by slash-separated
// segments, producing names like "payments/checkout" for hierarchical naming.
func (parser *Parser) expectHierarchicalName() (string, error) {
	firstSegment, err := parser.expectIdentifier()
	if err != nil {
		return "", err
	}
	name := firstSegment
	for parser.currentTokenIs(TokenSlash) {
		parser.advanceToken() // consume slash
		nextSegment, segmentErr := parser.expectIdentifier()
		if segmentErr != nil {
			return "", segmentErr
		}
		name += "/" + nextSegment
	}
	return name, nil
}

// expectStringOrIdentifier consumes and returns the current token's value if
// it is either a quoted string or an identifier.
func (parser *Parser) expectStringOrIdentifier() (string, error) {
	token := parser.currentToken()
	if token.Type == TokenString {
		parser.advanceToken()
		return token.Value, nil
	}
	return parser.expectIdentifier()
}

// parseConfigBlock parses a config { ... } block containing env and file entries.
func (parser *Parser) parseConfigBlock() (*ConfigDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	configDecl := &ConfigDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "env":
			envName, err := parser.expectIdentifier()
			if err != nil {
				return nil, err
			}
			envValue, err := parser.expectStringOrIdentifier()
			if err != nil {
				return nil, err
			}
			configDecl.EnvVars = append(configDecl.EnvVars, EnvVarDecl{
				Name: envName, Value: envValue,
			})
		case "file":
			filePath, err := parser.expectStringOrIdentifier()
			if err != nil {
				return nil, err
			}
			fileContent, err := parser.expectStringOrIdentifier()
			if err != nil {
				return nil, err
			}
			configDecl.ConfigFiles = append(configDecl.ConfigFiles, ConfigFileDecl{
				Path: filePath, Content: fileContent,
			})
		default:
			return nil, parser.parserErrorf("unknown config field %q", key)
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return configDecl, nil
}

// parseInitStepBlock parses an init { exec "cmd" timeout 30s retry 5 } block.
// Init steps define sequential initialization commands that must complete before
// the main workload starts.
func (parser *Parser) parseInitStepBlock() (InitStepDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return InitStepDecl{}, err
	}
	parser.skipNewlineTokens()

	initStepDecl := InitStepDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return InitStepDecl{}, err
		}

		switch key {
		case "exec":
			initStepDecl.Exec, err = parser.expectStringOrIdentifier()
		case "timeout":
			token := parser.currentToken()
			if token.Type != TokenNumber && token.Type != TokenIdent {
				return InitStepDecl{}, parser.parserErrorf("expected duration for timeout, got %s", token.Type)
			}
			initStepDecl.Timeout = token.Value
			parser.advanceToken()
		case "retry":
			initStepDecl.Retry, err = parser.expectInteger()
		default:
			return InitStepDecl{}, parser.parserErrorf("unknown init field %q", key)
		}
		if err != nil {
			return InitStepDecl{}, err
		}

		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return InitStepDecl{}, err
	}

	if initStepDecl.Exec == "" {
		return InitStepDecl{}, parser.parserErrorf("init block requires an exec command")
	}

	return initStepDecl, nil
}

// parseSecretDeclaration parses a single "secret NAME [PATH]" entry in a
// service block.
func (parser *Parser) parseSecretDeclaration() (SecretDecl, error) {
	secretName, err := parser.expectIdentifier()
	if err != nil {
		return SecretDecl{}, err
	}

	mountPath := "/run/secrets/" + secretName
	if parser.currentToken().Type == TokenString || parser.currentToken().Type == TokenIdent {
		if parser.currentToken().Type == TokenString {
			mountPath = parser.currentToken().Value
			parser.advanceToken()
		}
	}

	return SecretDecl{Name: secretName, MountPath: mountPath}, nil
}

// expectResourceValue consumes a token that can be a number with unit suffix
// (e.g. "256Gi", "10Ti"), a quoted string, or an identifier.
func (parser *Parser) expectResourceValue() (string, error) {
	token := parser.currentToken()
	if token.Type == TokenNumber || token.Type == TokenString || token.Type == TokenIdent {
		parser.advanceToken()
		return token.Value, nil
	}
	return "", parser.parserErrorf("expected resource value, got %s %q", token.Type, token.Value)
}

// parseTenantDeclaration parses a tenant block with optional quota and weight.
func (parser *Parser) parseTenantDeclaration() (*TenantDecl, error) {
	line := parser.currentToken().Line
	parser.advanceToken() // skip "tenant"

	name, err := parser.expectIdentifier()
	if err != nil {
		return nil, err
	}

	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	tenantDecl := &TenantDecl{Name: name, Line: line}

	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "quota":
			tenantDecl.Quota, err = parser.parseQuotaBlock()
		case "weight":
			tenantDecl.Weight, err = parser.expectInteger()
		default:
			return nil, parser.parserErrorf("unknown tenant field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return tenantDecl, nil
}

// parseQuotaBlock parses a quota { cpu N, memory X, instances N, volumes N, storage X } block.
func (parser *Parser) parseQuotaBlock() (*QuotaDecl, error) {
	if err := parser.expectToken(TokenLBrace); err != nil {
		return nil, err
	}
	parser.skipNewlineTokens()

	quotaDecl := &QuotaDecl{}
	for !parser.currentTokenIs(TokenRBrace) && !parser.isAtEnd() {
		key, err := parser.expectIdentifier()
		if err != nil {
			return nil, err
		}

		switch key {
		case "cpu":
			quotaDecl.CPU, err = parser.expectInteger()
		case "memory":
			quotaDecl.Memory, err = parser.expectResourceValue()
		case "instances":
			quotaDecl.Instances, err = parser.expectInteger()
		case "volumes":
			quotaDecl.Volumes, err = parser.expectInteger()
		case "storage":
			quotaDecl.Storage, err = parser.expectResourceValue()
		default:
			return nil, parser.parserErrorf("unknown quota field %q", key)
		}
		if err != nil {
			return nil, err
		}
		parser.skipNewlineTokens()
	}

	if err := parser.expectToken(TokenRBrace); err != nil {
		return nil, err
	}
	return quotaDecl, nil
}

// parserErrorf returns a formatted error that includes the current token's
// line and column for diagnostic context.
func (parser *Parser) parserErrorf(format string, args ...any) error {
	token := parser.currentToken()
	return fmt.Errorf("line %d col %d: %s", token.Line, token.Col, fmt.Sprintf(format, args...))
}
