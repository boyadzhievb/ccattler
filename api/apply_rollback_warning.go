// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boyadzhievb/ccattler/lang"
	"github.com/boyadzhievb/ccattler/types"
)

// collectRolledBackImageWarnings returns one warning per compiled service whose
// image is the image its last rollback moved away from (M241 D9), naming the
// service, the image, how long ago and why. It never refuses: a fixed image
// may reuse the tag. Services are listed in name order.
func (apiServer *Server) collectRolledBackImageWarnings(requestContext context.Context, facts []lang.Fact) []string {
	compiledImageByService := make(map[string]string)
	for _, compiledFact := range facts {
		serviceName, isServiceImage := strings.CutPrefix(compiledFact.Key, types.ScanDesiredServices)
		serviceName, hasImageSuffix := strings.CutSuffix(serviceName, "/image")
		if isServiceImage && hasImageSuffix && compiledFact.Key == types.KeyDesiredServiceImage(serviceName) {
			compiledImageByService[serviceName] = compiledFact.Value
		}
	}
	serviceNames := make([]string, 0, len(compiledImageByService))
	for serviceName := range compiledImageByService {
		serviceNames = append(serviceNames, serviceName)
	}
	sort.Strings(serviceNames)

	var warnings []string
	for _, serviceName := range serviceNames {
		rolledBackImage := apiServer.readFactValueOrEmpty(requestContext, types.KeyDerivedServiceRolloutRolledBackImage(serviceName))
		if rolledBackImage == "" || rolledBackImage != compiledImageByService[serviceName] {
			continue
		}
		rolledBackWhen := "earlier"
		rolledBackAt, parseError := time.Parse(time.RFC3339, apiServer.readFactValueOrEmpty(requestContext, types.KeyDerivedServiceRolloutRolledBackAt(serviceName)))
		if parseError == nil {
			rolledBackWhen = types.FormatCompactDuration(time.Since(rolledBackAt)) + " ago"
		}
		warnings = append(warnings, fmt.Sprintf("service %s: image %s was rolled back %s (%s); applying it starts a new rollout",
			serviceName, rolledBackImage, rolledBackWhen,
			apiServer.readFactValueOrEmpty(requestContext, types.KeyDerivedServiceRolloutRollbackReason(serviceName))))
	}
	return warnings
}

// readFactValueOrEmpty returns the stored value of factKey, or "" when it is
// missing or cannot be read.
func (apiServer *Server) readFactValueOrEmpty(requestContext context.Context, factKey string) string {
	fact, getError := apiServer.factStore.Get(requestContext, factKey)
	if getError != nil {
		return ""
	}
	return string(fact.Value)
}
