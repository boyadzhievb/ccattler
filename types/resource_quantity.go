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
	// MillicoresPerCore converts a CPU value in cores to millicores (also the
	// lang bare-CPU warning threshold, M243 D10).
	MillicoresPerCore = 1000

	// millicoreSuffix marks a CPU value written in millicores ("250m").
	millicoreSuffix = "m"

	// mebibyteSuffix is the unit the vertical autoscaler writes memory in.
	mebibyteSuffix = "Mi"

	// kibibyteSuffix and kilobyteSuffix write an exact memory max under 1 MiB
	// (FormatMemoryBytesExactly, D9).
	kibibyteSuffix = "Ki"
	kilobyteSuffix = "k"

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
	return parseScaledDecimal(cpuText, "cpu", cpuText, MillicoresPerCore)
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

// parseScaledDecimal multiplies the decimal numberText ("12", "1.25", ".5") by
// multiplier with exact integer arithmetic, rounds a fraction up to the next
// whole unit, and returns it when it fits int64. fullText and resourceName
// name the value in errors.
func parseScaledDecimal(fullText string, resourceName string, numberText string, multiplier int64) (int64, error) {
	if len(fullText) > maxResourceQuantityLength {
		return 0, fmt.Errorf("%s %q: too long", resourceName, fullText)
	}
	integerDigits, fractionDigits, hasFraction := strings.Cut(numberText, ".")
	hasLeadingDot := hasFraction && integerDigits == "" // ".5", as Kubernetes accepts (D2); "5." stays refused
	if (!hasLeadingDot && !IsBareInteger(integerDigits)) || (hasFraction && !IsBareInteger(fractionDigits)) {
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

// FormatMemoryBytesExactly writes bytes exactly in unit form (D9): whole KiB as
// "{n}Ki", otherwise decimal "k" with up to 3 decimals ("524.289k"). Never bare
// digits; ParseMemoryBytes reads it back to the same bytes.
func FormatMemoryBytesExactly(memoryBytes int64) string {
	if memoryBytes%kibibyteMultiplier == 0 {
		return strconv.FormatInt(memoryBytes/kibibyteMultiplier, decimalBase) + kibibyteSuffix
	}
	wholeKilobytes := strconv.FormatInt(memoryBytes/kilobyteMultiplier, decimalBase)
	fractionDigits := strings.TrimRight(fmt.Sprintf("%03d", memoryBytes%kilobyteMultiplier), "0")
	if fractionDigits == "" {
		return wholeKilobytes + kilobyteSuffix
	}
	return wholeKilobytes + "." + fractionDigits + kilobyteSuffix
}

// FormatMebibytes writes a whole number of MiB in unit form ("768Mi"); the
// vertical autoscaler computes memory in whole MiB (D7).
func FormatMebibytes(mebibytes int64) string {
	return strconv.FormatInt(mebibytes, decimalBase) + mebibyteSuffix
}

// MemoryBytesToMiBRoundedDown converts bytes to whole MiB, rounding down, so
// a maximum bound that is not whole MiB is never exceeded (D7).
func MemoryBytesToMiBRoundedDown(memoryBytes int64) int64 {
	return memoryBytes / mebibyteMultiplier
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

// OccurrenceSet remembers identities already seen, so a log-once cache can ask
// whether this is the first time. Safe for concurrent use; the zero value is
// empty and ready.
type OccurrenceSet struct {
	seenIdentities sync.Map // seenIdentities holds every identity passed to IsFirstOccurrence
}

// IsFirstOccurrence records the identity built from parts (joined with NUL, so
// "a"+"bc" and "ab"+"c" differ) and reports whether it was new.
func (occurrenceSet *OccurrenceSet) IsFirstOccurrence(identityParts ...string) bool {
	_, alreadySeen := occurrenceSet.seenIdentities.LoadOrStore(strings.Join(identityParts, "\x00"), true)
	return !alreadySeen
}

// unreadableResourceWarnings remembers the reader/key/value triples already
// logged by WarnUnreadableResourceOnce.
var unreadableResourceWarnings OccurrenceSet

// WarnUnreadableResourceOnce logs, once per reader, key and value, that a
// stored resource value cannot be parsed, with what the reader does instead.
// Every reader of requests uses it, so a bad value does not flood the log.
func WarnUnreadableResourceOnce(readerName string, factKey string, factValue string, parseError error, consequence string) {
	if !unreadableResourceWarnings.IsFirstOccurrence(readerName, factKey, factValue) {
		return
	}
	logging.Default().Warn("unreadable resource value; re-apply the configuration to fix it",
		"reader", readerName, "key", factKey, "value", factValue,
		"error", parseError.Error(), "consequence", consequence)
}
