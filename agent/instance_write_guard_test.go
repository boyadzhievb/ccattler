// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// typesImportPath is the import path of the types package, whose key and
// writer functions the scan recognises under the file's own name for it.
const typesImportPath = "github.com/boyadzhievb/ccattler/types"

// transactionWriter is an agent function allowed to call a store Transaction.
type transactionWriter struct {
	reason           string // reason says why its transactions are guarded or touch no instance key.
	transactionCount int    // transactionCount is how many Transaction calls it has (another one fails, 3g R6-2).
}

// transactionWriters are the only agent functions that may call a store
// Transaction (M267 3e, 3f closed-world rule in placement_guard.go), keyed by
// receiver type and name (3g R6-2). What their transactions write is checked
// on real store operations by the audit (instance_write_audit_test.go, 3g.0).
var transactionWriters = map[string]transactionWriter{
	"instanceFactWriter.writeGuarded":                {"the guarded writer (placementGuard on the scanned placement revision)", 1},
	"instanceFactWriter.deleteUnlessPlacedElsewhere": {"cleanup of an instance that left the desired set, on placement/instance/{id}", 1},
	"Agent.publishInstanceStateUnlessStopped":        {"the guarded publish transaction (state revision and placementGuard)", 1},
	"Agent.createInstanceCreatedAtOnce":              {"create-only created-at, with placementGuard", 1},
	"Agent.markMovedInstancePending":                 {"exception: the source after a move, guarded by observed node and state revisions", 1},
	"Agent.publishRemovedInstanceState":              {"exception: the stopped write, on the placement/instance/{id} revision the move check read", 1},
	"Agent.recordVolumeHomeNode":                     {"not an instance key: observed/volume/{name}/home_node, create-only", 1},
	"NodeReporter.PublishAliveState":                 {"not an instance key: observed/node/{id}/state", 2},
	"VXLANReconciler.allocateNextFreeSubnet":         {"not an instance key: network/subnet/ allocation", 1},
}

// nonInstanceTypesWriters are the types package's store helpers the agent may
// call; any other types.Write*/types.Delete* could write instance facts.
var nonInstanceTypesWriters = map[string]bool{"WriteObservedVolume": true}

// instanceKeyFunctionPrefixes name the types key functions of per-instance
// keys: never allowed as a direct Put/Delete key (KeyNetworkAllocation, 3g R6-2).
var instanceKeyFunctionPrefixes = []string{"KeyObservedInstance", "KeyNetworkAllocation"}

// storeKeyMethods are store methods whose second argument is the key.
var storeKeyMethods = map[string]bool{"Put": true, "Delete": true, "PutWithLease": true}

// storeMethods are store methods that must always be called directly, never
// taken as a method value (which would hide the key from the scan).
var storeMethods = map[string]bool{"Put": true, "Delete": true, "PutWithLease": true, "Transaction": true}

// typesPackageName returns the name file gives the types package: its import
// alias, "types" without an alias or without the import; "." for a dot import.
func typesPackageName(file *ast.File) string {
	for _, importSpec := range file.Imports {
		importPath, unquoteError := strconv.Unquote(importSpec.Path.Value)
		if unquoteError == nil && importPath == typesImportPath && importSpec.Name != nil {
			return importSpec.Name.Name
		}
	}
	return "types"
}

// isTypesSelector reports whether selector is typesName.Something.
func isTypesSelector(selector *ast.SelectorExpr, typesName string) bool {
	packageIdent, isIdent := selector.X.(*ast.Ident)
	return isIdent && packageIdent.Name == typesName
}

// isPlainNonInstanceKey reports whether a store key argument is a direct
// types.Key* call that is not a per-instance key (instanceKeyFunctionPrefixes):
// the only key form the scan can prove is no instance fact.
func isPlainNonInstanceKey(keyArgument ast.Expr, typesName string) bool {
	call, isCall := keyArgument.(*ast.CallExpr)
	if !isCall {
		return false
	}
	selector, isSelector := call.Fun.(*ast.SelectorExpr)
	if !isSelector || !isTypesSelector(selector, typesName) || !strings.HasPrefix(selector.Sel.Name, "Key") {
		return false
	}
	for _, instanceKeyPrefix := range instanceKeyFunctionPrefixes {
		if strings.HasPrefix(selector.Sel.Name, instanceKeyPrefix) {
			return false
		}
	}
	return true
}

// scanStoreWrites reports every store write in node that the closed-world rule
// does not allow; enclosingFunction is the declaring function's key ("" for
// package-level code). It returns the violations and the Transaction count.
func scanStoreWrites(node ast.Node, enclosingFunction string, typesName string, where func(token.Pos) string) ([]string, int) {
	var violations []string
	transactionCount := 0
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
		if isTypesSelector(selector, typesName) {
			if (strings.HasPrefix(methodName, "Write") || strings.HasPrefix(methodName, "Delete")) && !nonInstanceTypesWriters[methodName] {
				violations = append(violations, where(call.Pos())+" types."+methodName)
			}
			return true
		}
		switch {
		case methodName == "Transaction":
			transactionCount++
			if _, allowed := transactionWriters[enclosingFunction]; enclosingFunction == "" || !allowed {
				violations = append(violations, where(call.Pos())+" Transaction")
			}
		case storeKeyMethods[methodName] && len(call.Args) >= 2 && !isPlainNonInstanceKey(call.Args[1], typesName):
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
	return violations, transactionCount
}

// functionKey returns "Receiver.name" for a method (pointer and type
// parameters stripped) and "name" for a function: transactionWriters' keys.
func functionKey(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	receiverType := function.Recv.List[0].Type
	if starExpr, isPointer := receiverType.(*ast.StarExpr); isPointer {
		receiverType = starExpr.X
	}
	switch genericType := receiverType.(type) {
	case *ast.IndexExpr:
		receiverType = genericType.X
	case *ast.IndexListExpr:
		receiverType = genericType.X
	}
	if receiverIdent, isIdent := receiverType.(*ast.Ident); isIdent {
		return receiverIdent.Name + "." + function.Name.Name
	}
	return function.Name.Name
}

// declaredNames returns the identifiers node declares (parameters, results,
// receivers, :=, var, const, range variables, types and functions), so a
// declaration that shadows the types package name is found.
func declaredNames(node ast.Node) []*ast.Ident {
	var declared []*ast.Ident
	addFields := func(fields *ast.FieldList) {
		if fields != nil {
			for _, field := range fields.List {
				declared = append(declared, field.Names...)
			}
		}
	}
	ast.Inspect(node, func(inner ast.Node) bool {
		switch declaration := inner.(type) {
		case *ast.FuncDecl:
			declared = append(declared, declaration.Name)
			addFields(declaration.Recv)
		case *ast.FuncType:
			addFields(declaration.Params)
			addFields(declaration.Results)
		case *ast.ValueSpec:
			declared = append(declared, declaration.Names...)
		case *ast.TypeSpec:
			declared = append(declared, declaration.Name)
		case *ast.AssignStmt:
			if declaration.Tok == token.DEFINE {
				for _, assigned := range declaration.Lhs {
					if assignedIdent, isIdent := assigned.(*ast.Ident); isIdent {
						declared = append(declared, assignedIdent)
					}
				}
			}
		case *ast.RangeStmt:
			for _, rangeVariable := range []ast.Expr{declaration.Key, declaration.Value} {
				if rangeIdent, isIdent := rangeVariable.(*ast.Ident); isIdent && declaration.Tok == token.DEFINE {
					declared = append(declared, rangeIdent)
				}
			}
		}
		return true
	})
	return declared
}

// findUnguardedInstanceWrites applies scanStoreWrites to every declaration of
// a parsed file, checks the allow-listed functions' Transaction counts, the
// types import (no dot import) and shadowing of its name (3g R6-2).
func findUnguardedInstanceWrites(fileSet *token.FileSet, file *ast.File) []string {
	typesName := typesPackageName(file)
	position := func(pos token.Pos) string {
		location := fileSet.Position(pos)
		return filepath.Base(location.Filename) + ":" + strconv.Itoa(location.Line)
	}
	var violations []string
	if typesName == "." {
		violations = append(violations, position(file.Package)+" dot import of the types package")
	}
	for _, declaredIdent := range declaredNames(file) {
		if declaredIdent.Name == typesName {
			violations = append(violations, position(declaredIdent.Pos())+" declares "+declaredIdent.Name+", shadowing the types package")
		}
	}
	for _, declaration := range file.Decls {
		enclosingFunction := ""
		function, isFunction := declaration.(*ast.FuncDecl)
		if isFunction {
			enclosingFunction = functionKey(function)
		}
		where := func(pos token.Pos) string { return position(pos) + " " + enclosingFunction + ":" }
		declarationViolations, transactionCount := scanStoreWrites(declaration, enclosingFunction, typesName, where)
		violations = append(violations, declarationViolations...)
		if allowedWriter, allowed := transactionWriters[enclosingFunction]; isFunction && allowed && transactionCount != allowedWriter.transactionCount {
			violations = append(violations, where(function.Pos())+" "+strconv.Itoa(transactionCount)+" Transaction calls, transactionWriters allows "+
				strconv.Itoa(allowedWriter.transactionCount))
		}
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
				declaredFunctions[functionKey(function)] = true
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

// reviewRound6BypassSources are the 6 scan bypasses of review round 6 (R6-2).
var reviewRound6BypassSources = map[string]string{
	"allocation key Delete": `func bypass(ctx context.Context, factStore store.StateStore) {
	_ = factStore.Delete(ctx, types.KeyNetworkAllocation("web-1"))
}`,
	"aliased types writer": `import typespkg "github.com/boyadzhievb/ccattler/types"
func bypass(ctx context.Context, factStore store.StateStore) {
	_ = typespkg.WriteInstance(ctx, factStore, typespkg.Instance{ID: "web-1", State: "running"})
}`,
	"dot-imported types writer": `import . "github.com/boyadzhievb/ccattler/types"
func bypass(ctx context.Context, factStore store.StateStore) {
	_ = WriteInstance(ctx, factStore, Instance{ID: "web-1", State: "running"})
}`,
	"allow-listed name on another receiver": `func (otherWriter *otherWriter) writeGuarded(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.Transaction(ctx, nil, []store.Op{{Type: store.OpPut, Key: types.KeyObservedInstanceState("web-1")}}, nil)
}`,
	"Transaction added inside an allow-listed function": `func (instanceFactWriter instanceFactWriter) writeGuarded(ctx context.Context, factStore store.StateStore) {
	_, _ = factStore.Transaction(ctx, placementGuard("node-1", placedInstanceInfo{}), nil, nil)
	_, _ = factStore.Transaction(ctx, nil, []store.Op{{Type: store.OpPut, Key: types.KeyObservedInstanceState("web-1")}}, nil)
}`,
	"local identifier named types": `func bypass(ctx context.Context, factStore store.StateStore) {
	types := instanceKeys{}
	_, _ = factStore.Put(ctx, types.KeyState("web-1"), nil)
}`,
}

// TestInstanceWriteScanDetectsBypasses is the scan's control: each of the 8
// bypass patterns of review round 5 (T1) and the 6 of round 6 (R6-2) is
// reported.
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
	maps.Copy(bypassSources, reviewRound6BypassSources)
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
