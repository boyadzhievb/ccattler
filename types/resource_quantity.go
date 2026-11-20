// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/boyadzhievb/ccattler/logging"
)

// Resource requests and limits in Kubernetes notation (M243, spec
// resource-requests). Every component reads them through this file, so one
// stored value has one meaning: CPU in millicores, memory in bytes.

const (
	// millicoresPerCore converts a CPU value in cores to millicores.
	millicoresPerCore = 1000

	// millicoreSuffix marks a CPU value written in millicores ("250m").
	millicoreSuffix = "m"

	// mebibyteSuffix is the unit the vertical autoscaler writes memory in.
	mebibyteSuffix = "Mi"

	// maxResourceQuantityLength bounds the text a parser looks at; an int64
	// has at most 19 digits, so anything longer is either padding or overflow.
	maxResourceQuantityLength = 64

	// decimalBase is the base of the written digits.
	decimalBase = 10
)

// Memory suffix multipliers: binary (powers of 1024) and decimal (powers of 1000).
const (
	kibibyteMultiplier = int64(1) << 10 // kibibyteMultiplier is the value of Ki.
	mebibyteMultiplier = int64(1) << 20 // mebibyteMultiplier is the value of Mi.
	gibibyteMultiplier = int64(1) << 30 // gibibyteMultiplier is the value of Gi.
	tebibyteMultiplier = int64(1) << 40 // tebibyteMultiplier is the value of Ti.
	pebibyteMultiplier = int64(1) << 50 // pebibyteMultiplier is the value of Pi.
	exbibyteMultiplier = int64(1) << 60 // exbibyteMultiplier is the value of Ei.

	kilobyteMultiplier = int64(1_000)                     // kilobyteMultiplier is the value of k.
	megabyteMultiplier = int64(1_000_000)                 // megabyteMultiplier is the value of M.
	gigabyteMultiplier = int64(1_000_000_000)             // gigabyteMultiplier is the value of G.
	terabyteMultiplier = int64(1_000_000_000_000)         // terabyteMultiplier is the value of T.
	petabyteMultiplier = int64(1_000_000_000_000_000)     // petabyteMultiplier is the value of P.
	exabyteMultiplier  = int64(1_000_000_000_000_000_000) // exabyteMultiplier is the value of E.
)

// memorySuffixMultipliers maps each accepted memory suffix to its byte value.
// Two-letter binary suffixes are matched before the one-letter decimal ones.
var memorySuffixMultipliers = map[string]int64{
	"Ki": kibibyteMultiplier, "Mi": mebibyteMultiplier, "Gi": gibibyteMultiplier,
	"Ti": tebibyteMultiplier, "Pi": pebibyteMultiplier, "Ei": exbibyteMultiplier,
	"k": kilobyteMultiplier, "M": megabyteMultiplier, "G": gigabyteMultiplier,
	"T": terabyteMultiplier, "P": petabyteMultiplier, "E": exabyteMultiplier,
}

// ParseCPUMillicores reads a CPU value: a decimal number with m (millicores,
// "250m") or without it (cores, "0.5", "2"). A value finer than 1 millicore is
// rounded up (D2); a sign, exponent, whitespace, other suffix or overflow is an error.
func ParseCPUMillicores(cpuText string) (int64, error) {
	if millicoreDigits, hasMillicoreSuffix := strings.CutSuffix(cpuText, millicoreSuffix); hasMillicoreSuffix {
		return parseScaledDecimal(cpuText, "cpu", millicoreDigits, 1)
	}
	return parseScaledDecimal(cpuText, "cpu", cpuText, millicoresPerCore)
}

// ParseMemoryBytes reads a memory value: a decimal number with an optional
// suffix (Ki Mi Gi Ti Pi Ei, k M G T P E) or plain bytes. A value that is not
// a whole number of bytes is rounded up (D2); any other form is an error.
func ParseMemoryBytes(memoryText string) (int64, error) {
	numberText, multiplier := splitMemorySuffix(memoryText)
	return parseScaledDecimal(memoryText, "memory", numberText, multiplier)
}

// splitMemorySuffix splits a memory value into its number and the multiplier
// of its suffix; without a known suffix the whole text is the number (bytes).
func splitMemorySuffix(memoryText string) (string, int64) {
	for suffixLength := 2; suffixLength >= 1; suffixLength-- {
		if len(memoryText) <= suffixLength {
			continue
		}
		suffix := memoryText[len(memoryText)-suffixLength:]
		if multiplier, known := memorySuffixMultipliers[suffix]; known {
			return memoryText[:len(memoryText)-suffixLength], multiplier
		}
	}
	return memoryText, 1
}

// parseScaledDecimal multiplies the decimal numberText ("12" or "1.25") by
// multiplier with exact integer arithmetic, rounds a fraction up to the next
// whole unit, and returns it when it fits int64. fullText and resourceName
// name the value in errors.
func parseScaledDecimal(fullText string, resourceName string, numberText string, multiplier int64) (int64, error) {
	if len(fullText) > maxResourceQuantityLength {
		return 0, fmt.Errorf("%s %q: too long", resourceName, fullText)
	}
	integerDigits, fractionDigits, hasFraction := strings.Cut(numberText, ".")
	if !IsBareInteger(integerDigits) || (hasFraction && !IsBareInteger(fractionDigits)) {
		return 0, fmt.Errorf("%s %q: not a number with a known unit", resourceName, fullText)
	}
	scaledValue, parsed := new(big.Int).SetString(integerDigits+fractionDigits, decimalBase)
	if !parsed {
		return 0, fmt.Errorf("%s %q: not a number", resourceName, fullText)
	}
	scaledValue.Mul(scaledValue, big.NewInt(multiplier))
	fractionScale := new(big.Int).Exp(big.NewInt(decimalBase), big.NewInt(int64(len(fractionDigits))), nil)
	quotient, remainder := new(big.Int).QuoRem(scaledValue, fractionScale, new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1)) // finer than the unit: round up, as Kubernetes does
	}
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("%s %q: too large", resourceName, fullText)
	}
	return quotient.Int64(), nil
}

// IsBareInteger reports whether text is one or more ASCII digits: a value
// written without unit, sign or fraction (a legacy recommendation, a bare CPU).
func IsBareInteger(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// FormatCPUMillicores writes millicores in unit form ("750m").
func FormatCPUMillicores(millicores int64) string {
	return strconv.FormatInt(millicores, decimalBase) + millicoreSuffix
}

// FormatMemoryMiB writes bytes as whole MiB rounded up ("768Mi"), so the
// written value never undercounts the request.
func FormatMemoryMiB(memoryBytes int64) string {
	return strconv.FormatInt(MemoryBytesToMiBRoundedUp(memoryBytes), decimalBase) + mebibyteSuffix
}

// MemoryBytesToMiBRoundedUp converts bytes to whole MiB, rounding up, so a
// request compared with node capacity (MiB) is never undercounted.
func MemoryBytesToMiBRoundedUp(memoryBytes int64) int64 {
	wholeMebibytes := memoryBytes / mebibyteMultiplier
	if memoryBytes%mebibyteMultiplier != 0 {
		wholeMebibytes++
	}
	return wholeMebibytes
}

// unreadableResourceWarnings remembers the reader/key/value triples already
// logged by WarnUnreadableResourceOnce.
var unreadableResourceWarnings sync.Map

// WarnUnreadableResourceOnce logs, once per reader, key and value, that a
// stored resource value cannot be parsed, with what the reader does instead.
// Every reader of requests uses it, so a bad value does not flood the log.
func WarnUnreadableResourceOnce(readerName string, factKey string, factValue string, parseError error, consequence string) {
	warningIdentity := readerName + "\x00" + factKey + "\x00" + factValue
	if _, alreadyLogged := unreadableResourceWarnings.LoadOrStore(warningIdentity, true); alreadyLogged {
		return
	}
	logging.Default().Warn("unreadable resource value; re-apply the configuration to fix it",
		"reader", readerName, "key", factKey, "value", factValue,
		"error", parseError.Error(), "consequence", consequence)
}
