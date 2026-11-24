// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// transactionWriters are the only agent functions that may call a store
// Transaction (M267 3e, 3f closed-world rule in placement_guard.go). Each entry
// says why its transaction is guarded or does not touch observed/instance.
var transactionWriters = map[string]string{
	"writeGuarded":                      "instanceFactWriter: the guarded writer (placementGuard on the scanned placement revision)",
	"deleteUnlessPlacedElsewhere":       "instanceFactWriter: cleanup of an instance that left the desired set, on placement/instance/{id}",
	"publishInstanceStateUnlessStopped": "the guarded publish transaction (state revision and placementGuard)",
	"createInstanceCreatedAtOnce":       "create-only created-at, with placementGuard",
	"markMovedInstancePending":          "exception: the source after a move, guarded by observed node and state revisions",
	"publishRemovedInstanceState":       "exception: the stopped write, on the placement/instance/{id} revision the move check read",
	"recordVolumeHomeNode":              "not an instance key: observed/volume/{name}/home_node, create-only",
	"PublishAliveState":                 "not an instance key: observed/node/{id}/state",
	"allocateNextFreeSubnet":            "not an instance key: network/subnet/ allocation",
}

// nonInstanceTypesWriters are the types package's store helpers the agent may
// call; any other types.Write*/types.Delete* could write instance facts.
var nonInstanceTypesWriters = map[string]bool{"WriteObservedVolume": true}

// storeKeyMethods are store methods whose second argument is the key.
var storeKeyMethods = map[string]bool{"Put": true, "Delete": true, "PutWithLease": true}

// storeMethods are store methods that must always be called directly, never
// taken as a method value (which would hide the key from the scan).
var storeMethods = map[string]bool{"Put": true, "Delete": true, "PutWithLease": true, "Transaction": true}

// isPlainNonInstanceKey reports whether a store key argument is a direct
// types.Key* call that is not a types.KeyObservedInstance* key: the only key
// form the scan can prove is no instance fact.
func isPlainNonInstanceKey(keyArgument ast.Expr) bool {
	call, isCall := keyArgument.(*ast.CallExpr)
	if !isCall {
		return false
	}
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return false
	}
	packageIdent, isIdent := selector.X.(*ast.Ident)
	return isIdent && packageIdent.Name == "types" && strings.HasPrefix(selector.Sel.Name, "Key") &&
		!strings.HasPrefix(selector.Sel.Name, "KeyObservedInstance")
}

// scanStoreWrites reports every store write in node that the closed-world rule
// does not allow, enclosingFunction being the declaring function ("" for
// package-level code). where formats a position.
func scanStoreWrites(node ast.Node, enclosingFunction string, where func(token.Pos) string) []string {
	var violations []string
	calledSelectors := make(map[*ast.SelectorExpr]bool)
	ast.Inspect(node, func(inner ast.Node) bool {
		call, isCall := inner.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		calledSelectors[selector] = true
		methodName := selector.Sel.Name
		if packageIdent, isIdent := selector.X.(*ast.Ident); isIdent && packageIdent.Name == "types" {
			if (strings.HasPrefix(methodName, "Write") || strings.HasPrefix(methodName, "Delete")) && !nonInstanceTypesWriters[methodName] {
				violations = append(violations, where(call.Pos())+" types."+methodName)
			}
			return true
		}
		switch {
		case methodName == "Transaction" && (enclosingFunction == "" || transactionWriters[enclosingFunction] == ""):
			violations = append(violations, where(call.Pos())+" Transaction")
		case storeKeyMethods[methodName] && len(call.Args) >= 2 && !isPlainNonInstanceKey(call.Args[1]):
			violations = append(violations, where(call.Pos())+" "+methodName+" with a key that is not a direct non-instance types.Key* call")
		}
		return true
	})
	ast.Inspect(node, func(inner ast.Node) bool {
		if selector, isSelector := inner.(*ast.SelectorExpr); isSelector && storeMethods[selector.Sel.Name] && !calledSelectors[selector] {
			violations = append(violations, where(selector.Pos())+" method value "+selector.Sel.Name)
		}
		return true
	})
	return violations
}

// findUnguardedInstanceWrites applies scanStoreWrites to every declaration of
// a parsed file and returns "file:line function: what" per violation.
func findUnguardedInstanceWrites(fileSet *token.FileSet, file *ast.File) []string {
	var violations []string
	for _, declaration := range file.Decls {
		enclosingFunction := ""
		if function, isFunction := declaration.(*ast.FuncDecl); isFunction {
			enclosingFunction = function.Name.Name
		}
		where := func(position token.Pos) string {
			location := fileSet.Position(position)
			return filepath.Base(location.Filename) + ":" + strconv.Itoa(location.Line) + " " + enclosingFunction + ":"
		}
		violations = append(violations, scanStoreWrites(declaration, enclosingFunction, where)...)
	}
	return violations
}

// TestAgentInstanceWritesAreGuarded: M267 3e/3f, closed world. In the agent's
// production sources every Transaction is in transactionWriters, every
// Put/Delete/PutWithLease key is a direct non-instance types.Key* call, no store
// method is used as a value and no other types writer is called.
func TestAgentInstanceWritesAreGuarded(t *testing.T) {
	sourceFiles, globError := filepath.Glob("*.go")
	if globError != nil {
		t.Fatal(globError)
	}
	fileSet := token.NewFileSet()
	var violations []string
	declaredFunctions := make(map[string]bool)
	for _, sourceFile := range sourceFiles {
		if strings.HasSuffix(sourceFile, "_test.go") {
			continue
		}
		parsedFile, parseError := parser.ParseFile(fileSet, sourceFile, nil, 0)
		if parseError != nil {
			t.Fatal(parseError)
		}
		for _, declaration := range parsedFile.Decls {
			if function, isFunction := declaration.(*ast.FuncDecl); isFunction {
				declaredFunctions[function.Name.Name] = true
			}
		}
		violations = append(violations, findUnguardedInstanceWrites(fileSet, parsedFile)...)
	}
	for allowedFunction := range transactionWriters {
		if !declaredFunctions[allowedFunction] {
			t.Errorf("transactionWriters lists %s, which no longer exists: remove the stale entry", allowedFunction)
		}
	}
	sort.Strings(violations)
	for _, violation := range violations {
		t.Errorf("store write outside the closed-world rule: %s (use instanceFactWriter, placement_guard.go)", violation)
	}
}

// TestInstanceWriteScanDetectsBypasses is the scan's control: each of the 8
// bypass patterns of review round 5 (T1) is reported.
func TestInstanceWriteScanDetectsBypasses(t *testing.T) {
	bypassSources := map[string]string{
		"op builders in a Transaction": `func bypass(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.Transaction(ctx, nil, []store.Op{instanceImageChange("web-1", "nginx")}, nil)
}`,
		"op.Key Put": `func bypass(ctx context.Context, factStore store.StateStore) {
	change := instanceImageChange("web-1", "nginx")
	_, _ = factStore.Put(ctx, change.Key, change.Value)
}`,
		"var key": `func bypass(ctx context.Context, factStore store.StateStore) {
	var stateKey = types.KeyObservedInstanceState("web-1")
	_, _ = factStore.Put(ctx, stateKey, nil)
}`,
		"local key helper": `func bypass(ctx context.Context, factStore store.StateStore) {
	_ = factStore.Delete(ctx, readinessKeyOf("web-1"))
}`,
		"Put wrapper with key param": `func putFact(ctx context.Context, factStore store.StateStore, key string) {
	_, _ = factStore.Put(ctx, key, nil)
}`,
		"string concat": `func bypass(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.Put(ctx, types.ScanObservedInstances+"web-1/state", nil)
}`,
		"package-level func literal": `var writeState = func(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.PutWithLease(ctx, types.KeyObservedInstanceState("web-1"), nil, 0)
}`,
		"method value": `func bypass(ctx context.Context, factStore store.StateStore) {
	put := factStore.Put
	_, _ = put(ctx, types.KeyObservedNodeAddress("node-1"), nil)
}`,
	}
	for patternName, bypassSource := range bypassSources {
		fileSet := token.NewFileSet()
		parsedFile, parseError := parser.ParseFile(fileSet, "bypass.go", "package agent\n"+bypassSource, 0)
		if parseError != nil {
			t.Fatalf("%s: %v", patternName, parseError)
		}
		if violations := findUnguardedInstanceWrites(fileSet, parsedFile); len(violations) == 0 {
			t.Errorf("bypass %q not detected", patternName)
		}
	}
}
