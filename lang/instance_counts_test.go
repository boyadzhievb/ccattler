// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package lang

import (
	"reflect"
	"testing"
)

// TestServiceInstanceCountsFromFacts takes the largest of instances, scale
// min and scale max per service, and ignores keys that only look like counts.
func TestServiceInstanceCountsFromFacts(t *testing.T) {
	file, parseError := Parse(`
service web {
  image nginx:1.28
  instances 3
}
service api {
  image api:1
  instances 2
  scale {
    horizontal {
      min 4
      max 700
      target cpu = 60%
    }
  }
}
`)
	if parseError != nil {
		t.Fatal(parseError)
	}
	facts, compileError := Compile(file)
	if compileError != nil {
		t.Fatal(compileError)
	}
	facts = append(facts, Fact{Key: "desired/service/web/config/instances", Value: "9999"})

	wantCounts := []ServiceInstanceCount{{ServiceName: "api", InstanceCount: 700}, {ServiceName: "web", InstanceCount: 3}}
	if gotCounts := ServiceInstanceCountsFromFacts(facts); !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Errorf("counts %+v, want %+v", gotCounts, wantCounts)
	}
}
