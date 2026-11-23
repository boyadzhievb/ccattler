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

// guardedInstanceWriters are the only agent functions that may write or delete
// observed/instance/{id}/* directly or run a transaction over such keys (M267
// 3e, rule in placement_guard.go). Each entry says why its write is guarded.
var guardedInstanceWriters = map[string]string{
	"writeGuardedInstanceFacts":         "the guarded writer: placementGuard on the scanned placement revision",
	"deleteFactsUnlessPlacedElsewhere":  "cleanup of an instance that left the desired set: guarded by placement/instance/{id}",
	"publishInstanceStateUnlessStopped": "the guarded publish transaction (state revision and placementGuard)",
	"createInstanceCreatedAtOnce":       "create-only created-at, with placementGuard",
	"markMovedInstancePending":          "exception: the source after a move, guarded by observed node and state revisions",
	"publishRemovedInstanceState":       "exception: the stopped write, decided after the move check (not moved elsewhere)",
}

// storeWriteMethods are the store methods that write a key given as their
// second argument.
var storeWriteMethods = map[string]bool{"Put": true, "Delete": true, "PutWithLease": true}

// isObservedInstanceKeyCall reports whether expression calls a
// types.KeyObservedInstance* key builder.
func isObservedInstanceKeyCall(expression ast.Expr) bool {
	call, isCall := expression.(*ast.CallExpr)
	if !isCall {
		return false
	}
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector {
		return false
	}
	packageIdent, isIdent := selector.X.(*ast.Ident)
	return isIdent && packageIdent.Name == "types" && strings.HasPrefix(selector.Sel.Name, "KeyObservedInstance")
}

// findUnguardedInstanceWrites returns "file:line function" for every store
// Put/Delete of an observed/instance key and every Transaction in a function
// that builds such keys, outside guardedInstanceWriters.
func findUnguardedInstanceWrites(t *testing.T, fileSet *token.FileSet, file *ast.File) []string {
	t.Helper()
	var violations []string
	for _, declaration := range file.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Body == nil || guardedInstanceWriters[function.Name.Name] != "" {
			continue
		}
		instanceKeyVariables := make(map[string]bool)
		buildsInstanceKeys := false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if assignment, isAssignment := node.(*ast.AssignStmt); isAssignment && len(assignment.Lhs) == len(assignment.Rhs) {
				for index, value := range assignment.Rhs {
					if identifier, isIdent := assignment.Lhs[index].(*ast.Ident); isIdent && isObservedInstanceKeyCall(value) {
						instanceKeyVariables[identifier.Name] = true
					}
				}
			}
			if expression, isExpression := node.(ast.Expr); isExpression && isObservedInstanceKeyCall(expression) {
				buildsInstanceKeys = true
			}
			return true
		})
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall {
				return true
			}
			selector, isSelector := call.Fun.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			location := fileSet.Position(call.Pos())
			where := filepath.Base(location.Filename) + ":" + strconv.Itoa(location.Line) + " " + function.Name.Name
			switch {
			case storeWriteMethods[selector.Sel.Name] && len(call.Args) >= 2:
				keyArgument := call.Args[1]
				identifier, isIdent := keyArgument.(*ast.Ident)
				if isObservedInstanceKeyCall(keyArgument) || (isIdent && instanceKeyVariables[identifier.Name]) {
					violations = append(violations, where+" ("+selector.Sel.Name+")")
				}
			case selector.Sel.Name == "Transaction" && buildsInstanceKeys:
				violations = append(violations, where+" (Transaction)")
			}
			return true
		})
	}
	return violations
}

// TestAgentInstanceWritesAreGuarded: M267 3e. Scans the agent's production
// sources: every write or delete of an observed/instance/{id}/* key goes
// through the guarded helpers or one of the listed exceptions, so a source
// agent that is behind can never write onto an instance that moved away.
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
		violations = append(violations, findUnguardedInstanceWrites(t, fileSet, parsedFile)...)
	}
	for allowedFunction := range guardedInstanceWriters {
		if !declaredFunctions[allowedFunction] {
			t.Errorf("guardedInstanceWriters lists %s, which no longer exists: remove the stale exception", allowedFunction)
		}
	}
	sort.Strings(violations)
	for _, violation := range violations {
		t.Errorf("unguarded observed/instance write: %s; use writeGuardedInstanceFacts (placement_guard.go)", violation)
	}
}

// TestInstanceWriteScanDetectsViolations is the scan's positive control: a
// plain Put of an observed/instance key, a Delete through a key variable and a
// Transaction over such keys are all reported.
func TestInstanceWriteScanDetectsViolations(t *testing.T) {
	const violatingSource = `package agent
func writeDirectly(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.Put(ctx, types.KeyObservedInstanceState("web-1"), []byte("running"))
	readinessKey := types.KeyObservedInstanceProbeState("web-1", types.ProbeReadiness)
	_ = factStore.Delete(ctx, readinessKey)
	_, _ = factStore.Transaction(ctx, nil, []store.Op{{Key: types.KeyObservedInstanceImage("web-1")}}, nil)
}
`
	fileSet := token.NewFileSet()
	parsedFile, parseError := parser.ParseFile(fileSet, "violating.go", violatingSource, 0)
	if parseError != nil {
		t.Fatal(parseError)
	}
	if violations := findUnguardedInstanceWrites(t, fileSet, parsedFile); len(violations) != 3 {
		t.Errorf("scan found %v, want the Put, the Delete and the Transaction", violations)
	}
}
