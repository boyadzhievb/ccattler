package lang

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add(`service web {
    image nginx:1.27
    instances 3
    expose 8080
}`)
	f.Add(`service api {
    image myapp:v3
    instances 1
    expose 9090

    resources {
        cpu 500m
        memory 512Mi
    }

    health {
        http /health
        every 10s
    }
}`)
	f.Add(`tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
    }
}`)
	f.Add(`config api {
    env "LOG_LEVEL" = "info"
    file "/etc/config.yaml" = "key: value"
}`)
	f.Add(`secret database.password`)
	f.Add(`network {
    allow frontend/web -> payments/checkout port 443
    deny frontend/web -> payments/database
}`)
	f.Add(`role developer {
    allow service.read
    allow service.update
}`)
	f.Add(`grant developer to group developers`)
	f.Add(``)
	f.Add(`{{{{{`)
	f.Add(`service`)
	f.Add(`service web { image }`)

	f.Fuzz(func(t *testing.T, input string) {
		Parse(input)
	})
}

// FuzzTemplateRender exercises the template rendering pipeline with arbitrary
// template strings and values content. It verifies that no combination of inputs
// causes a panic — rendering errors are acceptable since most random inputs are
// invalid templates or malformed values.
func FuzzTemplateRender(f *testing.F) {
	// Seed corpus with valid templates and corresponding values content.
	f.Add(`service {{ .name }} { image {{ .image }} instances {{ .count }} }`,
		"name: web\nimage: nginx:1.27\ncount: 3")
	f.Add(`{{ default "latest" .tag }}`, "tag: v1.0")
	f.Add(`{{ required "need image" .image }}`, "image: nginx")
	f.Add(`{{ quote .name }}`, "name: test")
	f.Add(`{{ upper .env }}`, "env: production")
	f.Add(`no placeholders at all`, "key: value")
	f.Add(`{{ .missing }}`, "other: stuff")
	f.Add(``, "")
	f.Add(`{{ if .enabled }}yes{{ else }}no{{ end }}`, "enabled: true")
	f.Add(`{{ lower .x }} {{ upper .y }}`, "x: HELLO\ny: world")

	f.Fuzz(func(fuzzTest *testing.T, templateContent string, valuesContent string) {
		// Parse values content using the same key:value format as LoadValuesFile.
		valuesMap := make(map[string]any)
		for _, rawLine := range strings.Split(valuesContent, "\n") {
			trimmedLine := strings.TrimSpace(rawLine)
			if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
				continue
			}
			colonIndex := strings.IndexByte(trimmedLine, ':')
			if colonIndex < 0 {
				continue // skip malformed lines
			}
			keyPart := strings.TrimSpace(trimmedLine[:colonIndex])
			valuePart := strings.TrimSpace(trimmedLine[colonIndex+1:])
			if keyPart != "" {
				valuesMap[keyPart] = valuePart
			}
		}

		// Render — errors are expected for random inputs, panics are bugs.
		_, _ = RenderTemplate(templateContent, valuesMap)
	})
}

func FuzzLexer(f *testing.F) {
	f.Add(`service web { image nginx:1.27 instances 3 }`)
	f.Add(`"string with \"escapes\" and \n newlines"`)
	f.Add(`12345 67890 3.14`)
	f.Add(`# comment line`)
	f.Add(`= { } -> >= <= != ==`)

	f.Fuzz(func(t *testing.T, input string) {
		lexer := NewLexer(input)
		lexer.Tokenize()
	})
}
