package lang

import (
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
