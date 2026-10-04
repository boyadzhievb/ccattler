// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"testing"
)

// FuzzCompiler feeds arbitrary strings through Parse then Compile, asserting
// that the compiler never panics on any valid AST and that every emitted fact
// carries a non-empty Key.
func FuzzCompiler(fuzz *testing.F) {
	// Seed corpus: valid DSL examples covering the major declaration types.
	fuzz.Add(`service web {
    image nginx:1.27
    instances 3
    expose 8080
}`)
	fuzz.Add(`tenant payments {
    quota {
        cpu 100
        memory 256Gi
        instances 500
    }
}`)
	fuzz.Add(`config api {
    env "LOG_LEVEL" = "info"
    file "/etc/config.yaml" = "key: value"
}`)
	fuzz.Add(`secret database.password`)
	fuzz.Add(`volume database {
    size 100Gi
    persistent true
}`)

	// Seed corpus: malformed and edge-case inputs that exercise error paths.
	fuzz.Add(``)
	fuzz.Add(`service`)
	fuzz.Add(`service web {}`)
	fuzz.Add(`service web { image }`)
	fuzz.Add(`{{{{{`)
	fuzz.Add(`tenant`)
	fuzz.Add(`volume`)
	fuzz.Add(`config`)
	fuzz.Add(`secret`)
	fuzz.Add(`service web { instances -1 }`)
	fuzz.Add(`service web { image nginx:1.27 instances 0 expose 0 }`)

	fuzz.Fuzz(func(test *testing.T, input string) {
		// Parse the input; if parsing fails the input is not a valid AST,
		// so skip it since the compiler only accepts parsed files.
		parsedFile, parseError := Parse(input)
		if parseError != nil {
			return
		}

		// Compile the parsed AST. The implicit assertion is that Compile
		// does not panic on any successfully parsed input.
		compiledFacts, compileError := Compile(parsedFile)
		if compileError != nil {
			return
		}

		// Every emitted fact must have a non-empty Key field; an empty key
		// would be meaningless in the fact store.
		for _, fact := range compiledFacts {
			if fact.Key == "" {
				test.Errorf("compiler produced a fact with an empty Key; value=%q", fact.Value)
			}
		}
	})
}
