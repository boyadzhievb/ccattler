package lang

import "testing"

func TestLexBasicService(t *testing.T) {
	input := `service web {
    image nginx:1.28
    instances 3
    expose 8080
}`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	expected := []struct {
		typ TokenType
		val string
	}{
		{TokenIdent, "service"},
		{TokenIdent, "web"},
		{TokenLBrace, "{"},
		{TokenNewline, "\n"},
		{TokenIdent, "image"},
		{TokenIdent, "nginx:1.28"},
		{TokenNewline, "\n"},
		{TokenIdent, "instances"},
		{TokenNumber, "3"},
		{TokenNewline, "\n"},
		{TokenIdent, "expose"},
		{TokenNumber, "8080"},
		{TokenNewline, "\n"},
		{TokenRBrace, "}"},
		{TokenEOF, ""},
	}

	if len(tokens) != len(expected) {
		t.Fatalf("expected %d tokens, got %d", len(expected), len(tokens))
	}
	for index, expectedToken := range expected {
		if tokens[index].Type != expectedToken.typ || tokens[index].Value != expectedToken.val {
			t.Errorf("token %d: got (%s, %q), want (%s, %q)",
				index, tokens[index].Type, tokens[index].Value, expectedToken.typ, expectedToken.val)
		}
	}
}

func TestLexResourceUnits(t *testing.T) {
	input := `cpu 500m
memory 512Mi`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	vals := make(map[string]bool)
	for _, tok := range tokens {
		vals[tok.Value] = true
	}
	if !vals["500m"] {
		t.Error("expected token 500m")
	}
	if !vals["512Mi"] {
		t.Error("expected token 512Mi")
	}
}

func TestLexComments(t *testing.T) {
	input := `# this is a comment
service web {
    # another comment
    instances 3
}`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	// Comments should be skipped entirely.
	for _, tok := range tokens {
		if tok.Type == TokenIdent && tok.Value == "#" {
			t.Error("comment character should not appear as token")
		}
	}

	// Should still parse the service.
	found := false
	for _, tok := range tokens {
		if tok.Type == TokenIdent && tok.Value == "service" {
			found = true
		}
	}
	if !found {
		t.Error("service keyword not found")
	}
}

func TestLexNestedBlocks(t *testing.T) {
	input := `service web {
    health {
        http /health
        every 10s
    }
    resources {
        cpu 500m
        memory 512Mi
    }
}`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	braces := 0
	for _, tok := range tokens {
		if tok.Type == TokenLBrace {
			braces++
		}
		if tok.Type == TokenRBrace {
			braces--
		}
	}
	if braces != 0 {
		t.Errorf("unbalanced braces: %d", braces)
	}
}

func TestLexSlash(t *testing.T) {
	input := `http /health`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	if tokens[1].Type != TokenSlash {
		t.Errorf("expected TokenSlash, got %s", tokens[1].Type)
	}
	if tokens[2].Type != TokenIdent || tokens[2].Value != "health" {
		t.Errorf("expected ident 'health', got (%s, %q)", tokens[2].Type, tokens[2].Value)
	}
}

func TestLexLineNumbers(t *testing.T) {
	input := "service web {\n    instances 3\n}"
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	// "service" is on line 1
	if tokens[0].Line != 1 {
		t.Errorf("service line: got %d, want 1", tokens[0].Line)
	}
	// "instances" is on line 2
	for _, tok := range tokens {
		if tok.Value == "instances" && tok.Line != 2 {
			t.Errorf("instances line: got %d, want 2", tok.Line)
		}
	}
}

func TestLexUnterminatedString(t *testing.T) {
	input := `"unterminated`
	_, err := NewLexer(input).Tokenize()
	if err == nil {
		t.Fatal("expected error for unterminated string")
	}
}

func TestLexEmptyInput(t *testing.T) {
	tokens, err := NewLexer("").Tokenize()
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Type != TokenEOF {
		t.Fatalf("expected just EOF, got %d tokens", len(tokens))
	}
}

func TestLexPercentage(t *testing.T) {
	input := `target cpu = 60%`
	tokens, err := NewLexer(input).Tokenize()
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, tok := range tokens {
		if tok.Value == "60%" {
			found = true
		}
	}
	if !found {
		t.Error("expected 60% token")
	}
}
