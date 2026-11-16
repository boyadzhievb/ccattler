// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/tenant"
	"github.com/boyadzhievb/ccattler/types"
)

const (
	// scaleTestWarningThreshold is the count from which spec service-scaling warns.
	scaleTestWarningThreshold = 500
	// scaleTestApprovalLimit is the largest count spec service-scaling accepts without approval.
	scaleTestApprovalLimit = 600
	// scaleTestHugeCount is a deliberately large approved count (no ceiling).
	scaleTestHugeCount = 100000
	// scaleTestAuditCapacity is the in-memory audit log size for these tests.
	scaleTestAuditCapacity = 100
)

// failingTransactionStore refuses every transaction, to prove a store error
// is never answered as success.
type failingTransactionStore struct {
	store.StateStore // StateStore is the real store every other call goes to.
}

// Transaction always fails without applying anything.
func (failingStore *failingTransactionStore) Transaction(_ context.Context, _ []store.Compare, _ []store.Op, _ []store.Op) (bool, error) {
	return false, errors.New("injected store failure")
}

// removingTransactionStore deletes the service declaration right before the
// transaction, as a configuration change removing the service in between would.
type removingTransactionStore struct {
	store.StateStore        // StateStore is the real store every other call goes to.
	serviceName      string // serviceName is the declaration removed before each transaction.
}

// Transaction removes the declaration, then runs the real transaction.
func (removingStore *removingTransactionStore) Transaction(ctx context.Context, compares []store.Compare, onSuccess []store.Op, onFailure []store.Op) (bool, error) {
	if deleteError := removingStore.Delete(ctx, types.KeyDesiredService(removingStore.serviceName)); deleteError != nil {
		return false, deleteError
	}
	return removingStore.StateStore.Transaction(ctx, compares, onSuccess, onFailure)
}

// newScaleTestServer returns a server over factStore with admin (cluster.admin),
// scaler (scaling.write only), writer (workload.create only) and the audit log.
func newScaleTestServer(t *testing.T, factStore store.StateStore) (*Server, *security.InMemoryAuditLog) {
	t.Helper()
	apiServer := NewServer(factStore)
	apiAuthorizer := security.NewAPIAuthorizer()
	apiAuthorizer.Grant("user:admin", security.CapabilityClusterAdmin, security.ScopeCluster)
	apiAuthorizer.Grant("user:scaler", security.CapabilityScalingWrite, security.ScopeCluster)
	apiAuthorizer.Grant("user:writer", security.CapabilityWorkloadCreate, security.ScopeCluster)
	auditLog := security.NewInMemoryAuditLog(scaleTestAuditCapacity)
	apiAuthorizer.SetAuditLogger(auditLog)
	apiServer.SetAPIAuthorizer(apiAuthorizer)
	return apiServer, auditLog
}

// declareScaleTestService writes the declaration of serviceName with 1 instance.
func declareScaleTestService(t *testing.T, factStore store.StateStore, serviceName string) {
	t.Helper()
	ctx := context.Background()
	if _, putError := factStore.Put(ctx, types.KeyDesiredService(serviceName), []byte("")); putError != nil {
		t.Fatal(putError)
	}
	if _, putError := factStore.Put(ctx, types.KeyDesiredServiceInstances(serviceName), []byte("1")); putError != nil {
		t.Fatal(putError)
	}
}

// scaleRequestBody returns a scale body; approvedCount 0 means no approval.
func scaleRequestBody(serviceName string, instanceCount int, approvedCount int) string {
	if approvedCount == 0 {
		return fmt.Sprintf(`{"service":%q,"instances":%d}`, serviceName, instanceCount)
	}
	return fmt.Sprintf(`{"service":%q,"instances":%d,"approved_instances":%d}`, serviceName, instanceCount, approvedCount)
}

// storedInstanceCounts returns the desired and user-intent counts of
// serviceName, "" when a fact is absent.
func storedInstanceCounts(t *testing.T, factStore store.StateStore, serviceName string) (string, string) {
	t.Helper()
	ctx := context.Background()
	readCount := func(factKey string) string {
		fact, getError := factStore.Get(ctx, factKey)
		if getError != nil || fact.Value == nil {
			return ""
		}
		return string(fact.Value)
	}
	return readCount(types.KeyDesiredServiceInstances(serviceName)), readCount(types.KeyIntentUserServiceInstances(serviceName))
}

// assertScaleAnswer checks the HTTP status and that the body mentions every
// expected fragment.
func assertScaleAnswer(t *testing.T, response *http.Response, body string, wantStatus int, wantFragments ...string) {
	t.Helper()
	if response.StatusCode != wantStatus {
		t.Errorf("HTTP %d, want %d (body %s)", response.StatusCode, wantStatus, body)
	}
	for _, wantFragment := range wantFragments {
		if !strings.Contains(body, wantFragment) {
			t.Errorf("answer %s does not mention %q", body, wantFragment)
		}
	}
}

// scaleAsUser sends one scale request as userName and returns the response and body.
func scaleAsUser(apiServer *Server, userName string, requestBody string) (*http.Response, string) {
	recorder := serveAsUser(apiServer, userName, http.MethodPost, "/api/scale", requestBody)
	return recorder.Result(), recorder.Body.String()
}

// TestScaleRefusesUndeclaredService: spec service-scaling, "Scale only declared services".
func TestScaleRefusesUndeclaredService(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	apiServer, _ := newScaleTestServer(t, factStore)

	response, body := scaleAsUser(apiServer, "scaler", scaleRequestBody("nosuch", 2, 0))
	assertScaleAnswer(t, response, body, http.StatusNotFound, "nosuch")
	if desiredCount, intentCount := storedInstanceCounts(t, factStore, "nosuch"); desiredCount != "" || intentCount != "" {
		t.Errorf("refused scale wrote desired=%q intent=%q", desiredCount, intentCount)
	}
}

// TestScaleReportsStoreError: spec service-scaling, "Store write fails".
func TestScaleReportsStoreError(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	declareScaleTestService(t, memoryStore, "web")
	apiServer, _ := newScaleTestServer(t, &failingTransactionStore{StateStore: memoryStore})

	response, body := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", 3, 0))
	assertScaleAnswer(t, response, body, http.StatusInternalServerError, "error")
	if strings.Contains(body, `"ok":true`) {
		t.Errorf("store failure answered as success: %s", body)
	}
	if desiredCount, intentCount := storedInstanceCounts(t, memoryStore, "web"); desiredCount != "1" || intentCount != "" {
		t.Errorf("failed scale changed counts: desired=%q intent=%q", desiredCount, intentCount)
	}
}

// TestScaleConflictsWhenServiceChanges: spec service-scaling, "Service removed during the request".
func TestScaleConflictsWhenServiceChanges(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	defer memoryStore.Close()
	declareScaleTestService(t, memoryStore, "web")
	apiServer, _ := newScaleTestServer(t, &removingTransactionStore{StateStore: memoryStore, serviceName: "web"})

	response, body := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", 3, 0))
	assertScaleAnswer(t, response, body, http.StatusConflict, "web")
	if desiredCount, intentCount := storedInstanceCounts(t, memoryStore, "web"); desiredCount != "1" || intentCount != "" {
		t.Errorf("conflicting scale changed counts: desired=%q intent=%q", desiredCount, intentCount)
	}
}

// TestScaleWarnsFromThreshold: spec service-scaling, "Scale to the warning threshold".
func TestScaleWarnsFromThreshold(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	declareScaleTestService(t, factStore, "web")
	apiServer, _ := newScaleTestServer(t, factStore)

	quietResponse, quietBody := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", scaleTestWarningThreshold-1, 0))
	assertScaleAnswer(t, quietResponse, quietBody, http.StatusOK)
	if strings.Contains(quietBody, "warning") {
		t.Errorf("count below the threshold warned: %s", quietBody)
	}

	response, body := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", scaleTestWarningThreshold, 0))
	assertScaleAnswer(t, response, body, http.StatusOK, `"warning"`, fmt.Sprint(scaleTestWarningThreshold))
	limitResponse, limitBody := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", scaleTestApprovalLimit, 0))
	assertScaleAnswer(t, limitResponse, limitBody, http.StatusOK, `"warning"`)
	if desiredCount, intentCount := storedInstanceCounts(t, factStore, "web"); desiredCount != fmt.Sprint(scaleTestApprovalLimit) || intentCount != fmt.Sprint(scaleTestApprovalLimit) {
		t.Errorf("counts after scaling to the limit: desired=%q intent=%q", desiredCount, intentCount)
	}
}

// TestScaleAboveLimitNeedsApproval: spec service-scaling, "Above the limit without approval".
func TestScaleAboveLimitNeedsApproval(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	declareScaleTestService(t, factStore, "web")
	apiServer, _ := newScaleTestServer(t, factStore)

	for _, userName := range []string{"scaler", "admin"} {
		response, body := scaleAsUser(apiServer, userName, scaleRequestBody("web", scaleTestApprovalLimit+1, 0))
		assertScaleAnswer(t, response, body, http.StatusBadRequest, fmt.Sprint(scaleTestApprovalLimit), "approved_instances")
	}
	if desiredCount, _ := storedInstanceCounts(t, factStore, "web"); desiredCount != "1" {
		t.Errorf("refused scale changed the count to %q", desiredCount)
	}
}

// TestScaleApprovalMustRepeatCount: spec service-scaling, "Approval with a different count".
func TestScaleApprovalMustRepeatCount(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	declareScaleTestService(t, factStore, "web")
	apiServer, _ := newScaleTestServer(t, factStore)

	response, body := scaleAsUser(apiServer, "admin", scaleRequestBody("web", scaleTestHugeCount, scaleTestHugeCount/10))
	assertScaleAnswer(t, response, body, http.StatusBadRequest, fmt.Sprint(scaleTestHugeCount))
	if desiredCount, _ := storedInstanceCounts(t, factStore, "web"); desiredCount != "1" {
		t.Errorf("mismatched approval changed the count to %q", desiredCount)
	}
}

// TestScaleApprovalNeedsClusterAdmin: spec service-scaling, "Approval by a principal without cluster.admin".
func TestScaleApprovalNeedsClusterAdmin(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	declareScaleTestService(t, factStore, "web")
	apiServer, auditLog := newScaleTestServer(t, factStore)

	approvedCount := scaleTestApprovalLimit + 100
	response, body := scaleAsUser(apiServer, "scaler", scaleRequestBody("web", approvedCount, approvedCount))
	assertScaleAnswer(t, response, body, http.StatusForbidden, string(security.CapabilityClusterAdmin))
	if desiredCount, _ := storedInstanceCounts(t, factStore, "web"); desiredCount != "1" {
		t.Errorf("approval without cluster.admin changed the count to %q", desiredCount)
	}
	assertScaleApprovalAudited(t, auditLog, "user:scaler", approvedCount, security.AuditDecisionDeny)
}

// TestScaleApprovedAboveLimit: spec service-scaling, "Approved large count".
func TestScaleApprovedAboveLimit(t *testing.T) {
	factStore := store.NewMemoryStore()
	defer factStore.Close()
	declareScaleTestService(t, factStore, "web")
	apiServer, auditLog := newScaleTestServer(t, factStore)

	response, body := scaleAsUser(apiServer, "admin", scaleRequestBody("web", scaleTestHugeCount, scaleTestHugeCount))
	assertScaleAnswer(t, response, body, http.StatusOK, `"warning"`)
	var answer struct {
		OK        bool `json:"ok"`
		Instances int  `json:"instances"`
	}
	if decodeError := json.Unmarshal([]byte(body), &answer); decodeError != nil || !answer.OK || answer.Instances != scaleTestHugeCount {
		t.Errorf("approved scale answer %s (%v)", body, decodeError)
	}
	if desiredCount, intentCount := storedInstanceCounts(t, factStore, "web"); desiredCount != fmt.Sprint(scaleTestHugeCount) || intentCount != fmt.Sprint(scaleTestHugeCount) {
		t.Errorf("approved counts: desired=%q intent=%q", desiredCount, intentCount)
	}
	assertScaleApprovalAudited(t, auditLog, "user:admin", scaleTestHugeCount, security.AuditDecisionAllow)
}

// newApplyLimitTestServers returns two servers over fresh stores with the same
// users as newScaleTestServer: one applying through the policy gate, one direct.
func newApplyLimitTestServers(t *testing.T) map[string]struct {
	apiServer *Server
	factStore store.StateStore
} {
	t.Helper()
	serversByPath := map[string]struct {
		apiServer *Server
		factStore store.StateStore
	}{}
	for _, applyPath := range []string{"policy-gate", "direct"} {
		factStore := store.NewMemoryStore()
		t.Cleanup(func() { _ = factStore.Close() })
		apiServer, _ := newScaleTestServer(t, factStore)
		if applyPath == "policy-gate" {
			tenantRegistry := tenant.NewTenantRegistry(factStore)
			apiServer.SetPolicyGate(tenant.NewPolicyGate(factStore, tenantRegistry, tenant.NewQuotaAdmission(factStore, tenantRegistry),
				security.NewCapabilityFactAuthorizer(apiServer.apiAuthorizer), nil))
		}
		serversByPath[applyPath] = struct {
			apiServer *Server
			factStore store.StateStore
		}{apiServer, factStore}
	}
	return serversByPath
}

// applyAsUser sends configuration text as userName; approvedCount 0 means no approval.
func applyAsUser(apiServer *Server, userName string, configuration string, approvedCount int) (int, string) {
	requestBody := map[string]any{"config": configuration}
	if approvedCount != 0 {
		requestBody["approved_instances"] = approvedCount
	}
	encodedBody, _ := json.Marshal(requestBody)
	request := httptest.NewRequest(http.MethodPost, "/api/apply", bytes.NewReader(encodedBody))
	request.Header.Set("Content-Type", "application/json")
	userContext := security.WithPrincipalStruct(request.Context(), security.Principal{Kind: security.PrincipalKindUser, Name: userName})
	userContext = security.WithPrincipal(userContext, "user:"+userName)
	recorder := httptest.NewRecorder()
	apiServer.Handler().ServeHTTP(recorder, request.WithContext(userContext))
	return recorder.Code, recorder.Body.String()
}

// serviceConfiguration returns a one-service configuration body line set.
func serviceConfiguration(serviceName string, instanceCount int) string {
	return fmt.Sprintf("service %s {\n  image nginx:1.28\n  instances %d\n}\n", serviceName, instanceCount)
}

// TestApplyWarnsFromThreshold: spec service-scaling, "Apply a configuration at the threshold".
func TestApplyWarnsFromThreshold(t *testing.T) {
	for applyPath, testServer := range newApplyLimitTestServers(t) {
		statusCode, body := applyAsUser(testServer.apiServer, "admin", serviceConfiguration("web", scaleTestWarningThreshold+50), 0)
		if statusCode != http.StatusOK || !strings.Contains(body, "warning") || !strings.Contains(body, "web") {
			t.Errorf("%s: apply of %d instances: HTTP %d %s, want 200 with a warning naming web",
				applyPath, scaleTestWarningThreshold+50, statusCode, body)
		}
	}
}

// TestApplyAboveLimitNeedsApproval: spec service-scaling, "Apply above the limit".
func TestApplyAboveLimitNeedsApproval(t *testing.T) {
	aboveLimit := scaleTestApprovalLimit + 100
	configurations := map[string]string{
		"instances": serviceConfiguration("web", aboveLimit),
		"scale min": fmt.Sprintf("service web {\n  image nginx:1.28\n  scale {\n    horizontal {\n      min %d\n      max %d\n      target cpu = 60%%\n    }\n  }\n}\n", aboveLimit, aboveLimit),
		"scale max": fmt.Sprintf("service web {\n  image nginx:1.28\n  scale {\n    horizontal {\n      min 1\n      max %d\n      target cpu = 60%%\n    }\n  }\n}\n", aboveLimit),
	}
	for applyPath, testServer := range newApplyLimitTestServers(t) {
		for countSource, configuration := range configurations {
			statusCode, body := applyAsUser(testServer.apiServer, "admin", configuration, 0)
			if statusCode != http.StatusBadRequest || !strings.Contains(body, "web") || !strings.Contains(body, fmt.Sprint(aboveLimit)) {
				t.Errorf("%s, %s %d without approval: HTTP %d %s, want 400 naming web and %d",
					applyPath, countSource, aboveLimit, statusCode, body, aboveLimit)
			}
			if marker, _ := testServer.factStore.Get(context.Background(), types.KeyDesiredService("web")); marker != nil && marker.Revision != 0 {
				t.Errorf("%s, %s: refused apply wrote the declaration of web", applyPath, countSource)
			}
		}
	}
}

// TestApplyApprovalCoversOnlyThatCount: spec service-scaling, "Apply with approval".
func TestApplyApprovalCoversOnlyThatCount(t *testing.T) {
	approvedCount := scaleTestApprovalLimit + 100
	for applyPath, testServer := range newApplyLimitTestServers(t) {
		statusCode, body := applyAsUser(testServer.apiServer, "admin", serviceConfiguration("web", approvedCount), approvedCount)
		if statusCode != http.StatusOK {
			t.Errorf("%s: approved apply of %d: HTTP %d %s, want 200", applyPath, approvedCount, statusCode, body)
		}
		mixedConfiguration := serviceConfiguration("web", approvedCount) + serviceConfiguration("batch", approvedCount+100)
		statusCode, body = applyAsUser(testServer.apiServer, "admin", mixedConfiguration, approvedCount)
		if statusCode != http.StatusBadRequest || !strings.Contains(body, "batch") {
			t.Errorf("%s: approval of %d also let batch at %d through: HTTP %d %s", applyPath, approvedCount, approvedCount+100, statusCode, body)
		}
		statusCode, body = applyAsUser(testServer.apiServer, "writer", serviceConfiguration("web", approvedCount), approvedCount)
		if statusCode != http.StatusForbidden || !strings.Contains(body, string(security.CapabilityClusterAdmin)) {
			t.Errorf("%s: approval by a principal without cluster.admin: HTTP %d %s, want 403", applyPath, statusCode, body)
		}
	}
}

// assertScaleApprovalAudited checks that one scale.approve entry names the
// principal, the service web, the count and the decision.
func assertScaleApprovalAudited(t *testing.T, auditLog *security.InMemoryAuditLog, principalName string, approvedCount int, wantDecision string) {
	t.Helper()
	for _, entry := range auditLog.Entries() {
		if entry.Action == "scale.approve" && entry.Principal == principalName && entry.Target == "service/web" &&
			entry.Policy == fmt.Sprintf("instances=%d", approvedCount) && entry.Decision == wantDecision {
			return
		}
	}
	t.Errorf("no scale.approve audit entry for %s, service/web, instances=%d, %s; entries: %+v",
		principalName, approvedCount, wantDecision, auditLog.Entries())
}
