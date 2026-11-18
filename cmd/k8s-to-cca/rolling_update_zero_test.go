// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package main

import "testing"

// TestConvertRollingUpdateKeepsExplicitZero: review round 2 L3. An explicit
// maxUnavailable 0 converts to max_unavailable 0, not to an unset field (which
// would take the 25 % default, M241 D12).
func TestConvertRollingUpdateKeepsExplicitZero(t *testing.T) {
	t.Parallel()
	yamlInput := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: zero-down
spec:
  replicas: 4
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
`
	dslFile, convertError := convertKubernetesYAMLToDSL([]byte(yamlInput))
	if convertError != nil {
		t.Fatalf("conversion failed: %v", convertError)
	}
	updateDecl := dslFile.Services[0].Update
	if updateDecl == nil || updateDecl.MaxUnavailable == nil || *updateDecl.MaxUnavailable != 0 {
		t.Errorf("max_unavailable = %+v, want an explicit 0", updateDecl)
	}
	if updateDecl == nil || updateDecl.MaxExtra == nil || *updateDecl.MaxExtra != 1 {
		t.Errorf("max_extra = %+v, want 1", updateDecl)
	}
}
