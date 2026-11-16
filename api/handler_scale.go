// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

// POST /api/scale and the instance count limit shared with apply (M217,
// Flow 31, spec service-scaling).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/logging"
	"github.com/boyadzhievb/ccattler/security"
	"github.com/boyadzhievb/ccattler/store"
	"github.com/boyadzhievb/ccattler/types"
)

// scaleApprovalHint tells a scale API caller how to approve a count above the limit.
const scaleApprovalHint = "resend with approved_instances"

// scaleRequest is the JSON body for POST /api/scale.
type scaleRequest struct {
	Service           string `json:"service"`                      // Service is the declared service to scale.
	Instances         int    `json:"instances"`                    // Instances is the requested count.
	ApprovedInstances int    `json:"approved_instances,omitempty"` // ApprovedInstances repeats Instances to approve a count above the limit.
}

// scaleResponse is the JSON answer of a successful POST /api/scale.
type scaleResponse struct {
	OK        bool   `json:"ok"`                // OK is true once both count facts are stored.
	Service   string `json:"service"`           // Service is the scaled service.
	Instances int    `json:"instances"`         // Instances is the stored count.
	Warning   string `json:"warning,omitempty"` // Warning is set from the warning threshold on.
}

// handleScale serves POST /api/scale: a declared service only, the count
// limit with cluster.admin approval above it, and both count facts written
// in one transaction guarded by the service declaration.
func (apiServer *Server) handleScale(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSONError(responseWriter, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !apiServer.requireCapability(responseWriter, request, security.CapabilityScalingWrite, security.ScopeCluster) {
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, maxSmallRequestBodyBytes)
	var body scaleRequest
	if decodeError := json.NewDecoder(request.Body).Decode(&body); decodeError != nil {
		writeJSONError(responseWriter, "invalid JSON", http.StatusBadRequest)
		return
	}
	if body.Service == "" || body.Instances < 0 {
		writeJSONError(responseWriter, "service and non-negative instances required", http.StatusBadRequest)
		return
	}
	if validateError := types.ValidateResourceName(body.Service); validateError != nil {
		writeJSONError(responseWriter, validateError.Error(), http.StatusBadRequest)
		return
	}

	declaration, getError := apiServer.factStore.Get(request.Context(), types.KeyDesiredService(body.Service))
	if errors.Is(getError, store.ErrKeyNotFound) {
		writeJSONError(responseWriter, fmt.Sprintf("service %q is not declared; apply its configuration first", body.Service), http.StatusNotFound)
		return
	}
	if getError != nil {
		writeJSONError(responseWriter, "store: "+getError.Error(), http.StatusInternalServerError)
		return
	}
	warning, refusalStatus, refusalMessage := apiServer.checkInstanceCountApproval(request.Context(), "scale", body.Service, body.Instances, body.ApprovedInstances, scaleApprovalHint)
	if refusalStatus != 0 {
		writeJSONError(responseWriter, refusalMessage, refusalStatus)
		return
	}
	if writeStatus, writeMessage := apiServer.storeScaledInstanceCount(request.Context(), body.Service, body.Instances, declaration.Revision); writeStatus != 0 {
		writeJSONError(responseWriter, writeMessage, writeStatus)
		return
	}
	responseWriter.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(responseWriter).Encode(scaleResponse{OK: true, Service: body.Service, Instances: body.Instances, Warning: warning})
}

// storeScaledInstanceCount writes the desired and user-intent counts in one
// transaction that holds only while the declaration keeps declarationRevision.
// Returns 0 on success, else the HTTP status and message (409 or 500).
func (apiServer *Server) storeScaledInstanceCount(requestContext context.Context, serviceName string, instanceCount int, declarationRevision int64) (int, string) {
	countValue := []byte(strconv.Itoa(instanceCount))
	committed, transactionError := apiServer.factStore.Transaction(requestContext,
		[]store.Compare{{Key: types.KeyDesiredService(serviceName), Revision: declarationRevision}},
		[]store.Op{
			{Type: store.OpPut, Key: types.KeyDesiredServiceInstances(serviceName), Value: countValue},
			{Type: store.OpPut, Key: types.KeyIntentUserServiceInstances(serviceName), Value: countValue},
		}, nil)
	if transactionError != nil {
		logging.Default().Error("scale transaction failed", "service", serviceName, "error", transactionError.Error())
		return http.StatusInternalServerError, "store: scale not saved: " + transactionError.Error()
	}
	if !committed {
		return http.StatusConflict, fmt.Sprintf("service %q changed or was removed during the request; nothing was saved, try again", serviceName)
	}
	return 0, ""
}

// checkInstanceCountApproval applies the M217 limit to one service's count.
// It returns the warning to show, or a refusal (HTTP status and message):
// 400 without or with a mismatched approval, 403 when the approver is not
// cluster.admin. Every approval attempt above the limit is audited.
func (apiServer *Server) checkInstanceCountApproval(requestContext context.Context, action string, serviceName string, requestedCount int, approvedCount int, approvalHint string) (string, int, string) {
	decision := types.EvaluateServiceInstanceCount(requestedCount, approvedCount)
	decisionText := types.DescribeInstanceCountDecision(decision, serviceName, requestedCount, approvalHint)
	if decision.RefusesWithoutWrite() {
		if approvedCount != 0 {
			apiServer.recordInstanceApprovalAudit(requestContext, action, serviceName, requestedCount, security.AuditDecisionDeny)
		}
		return "", http.StatusBadRequest, decisionText
	}
	if decision == types.InstanceCountApproved {
		principal := security.PrincipalStructFromContext(requestContext)
		if authError := apiServer.apiAuthorizer.AuthorizeAPI(principal, security.CapabilityClusterAdmin, security.ScopeCluster, nil); authError != nil {
			apiServer.recordInstanceApprovalAudit(requestContext, action, serviceName, requestedCount, security.AuditDecisionDeny)
			return "", http.StatusForbidden, fmt.Sprintf("forbidden: approving %d instances for %s needs %s: %v",
				requestedCount, serviceName, security.CapabilityClusterAdmin, authError)
		}
		apiServer.recordInstanceApprovalAudit(requestContext, action, serviceName, requestedCount, security.AuditDecisionAllow)
	}
	return decisionText, 0, ""
}

// recordInstanceApprovalAudit writes one "<action>.approve" audit entry
// naming the principal, the service and the approved count (D7).
func (apiServer *Server) recordInstanceApprovalAudit(requestContext context.Context, action string, serviceName string, approvedCount int, decision string) {
	apiServer.apiAuthorizer.RecordAuditEntry(security.AuditEntry{
		Timestamp:  time.Now(),
		Principal:  security.PrincipalStructFromContext(requestContext).String(),
		Action:     action + ".approve",
		Target:     "service/" + serviceName,
		Decision:   decision,
		AuthMethod: security.AuthMethodFromContext(requestContext),
		Policy:     fmt.Sprintf("instances=%d", approvedCount),
	})
}

// applyApprovalHint tells an apply caller how to approve a count above the limit.
const applyApprovalHint = "rerun cca apply with --approve-above-limit"

// applyInstanceLimitCheck applies the M217 limit to every service of one
// apply request and keeps the warnings and the refusal for the answer.
type applyInstanceLimitCheck struct {
	apiServer      *Server         // apiServer runs the shared check and audit.
	requestContext context.Context // requestContext carries the principal.
	approvedCount  int             // approvedCount is the request's approved_instances, 0 for none.
	warnings       []string        // warnings collects one text per service at or above the threshold.
	refusalStatus  int             // refusalStatus is the HTTP status of a refusal, 0 when none.
}

// inspect checks every compiled service's largest count; the first refusal
// stops the apply (lang.ServiceInstanceCountsFromFacts sorts by name).
func (limitCheck *applyInstanceLimitCheck) inspect(facts []lang.Fact) error {
	for _, serviceCount := range lang.ServiceInstanceCountsFromFacts(facts) {
		warning, refusalStatus, refusalMessage := limitCheck.apiServer.checkInstanceCountApproval(limitCheck.requestContext,
			"apply", serviceCount.ServiceName, serviceCount.InstanceCount, limitCheck.approvedCount, applyApprovalHint)
		if refusalStatus != 0 {
			limitCheck.refusalStatus = refusalStatus
			return errors.New(refusalMessage)
		}
		if warning != "" {
			limitCheck.warnings = append(limitCheck.warnings, warning)
		}
	}
	return nil
}

// refusalStatusOr returns the limit check's refusal status, or fallbackStatus
// when the refusal came from another stage.
func (limitCheck *applyInstanceLimitCheck) refusalStatusOr(fallbackStatus int) int {
	if limitCheck.refusalStatus != 0 {
		return limitCheck.refusalStatus
	}
	return fallbackStatus
}
