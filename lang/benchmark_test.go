package lang

import (
	"testing"
)

const benchmarkServiceConfig = `service web {
    image nginx:1.27
    instances 3
    expose 8080

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }
}
`

const benchmarkFullConfig = `service web {
    image nginx:1.27
    instances 5
    expose 8080

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 512Mi
    }

    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60%
        }
    }

    placement {
        require gpu = true
        prefer region = us-east
    }
}

service api {
    image myapp:v3
    instances 2
    expose 9090

    resources {
        cpu 1
        memory 1Gi
    }
}

config api {
    env "LOG_LEVEL" = "info"
    env "PORT" = "9090"
}

secret database.password
`

func BenchmarkLexSingleService(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		lexer := NewLexer(benchmarkServiceConfig)
		lexer.Tokenize()
	}
}

func BenchmarkParseSingleService(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		Parse(benchmarkServiceConfig)
	}
}

func BenchmarkParseFullConfig(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		Parse(benchmarkFullConfig)
	}
}
