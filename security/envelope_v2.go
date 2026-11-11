// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package security

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// envelopeV2Magic starts every v2 envelope. A v1 envelope starts with a
// big-endian wrapped-DEK length below 16 MiB, so its first byte is always 0;
// the magic's non-zero first byte can never be mistaken for one.
var envelopeV2Magic = []byte{0xCC, 0xA7, 0x5E, 0xC7}

// envelopeV2Format is the format byte that follows the magic.
const envelopeV2Format byte = 2

// envelopeV2KEKVersionSize is the byte size of the KEK version field.
const envelopeV2KEKVersionSize = 4

// envelopeV2WrappedDEKLengthSize is the byte size of the wrapped DEK length.
const envelopeV2WrappedDEKLengthSize = 2

// envelopeV2HeaderSize is magic + format + KEK version + wrapped DEK length.
var envelopeV2HeaderSize = len(envelopeV2Magic) + 1 + envelopeV2KEKVersionSize + envelopeV2WrappedDEKLengthSize

// errEnvelopeV2Malformed is returned for a v2 envelope whose header or
// lengths are inconsistent.
var errEnvelopeV2Malformed = errors.New("malformed v2 envelope")

// isEnvelopeV2 reports whether an envelope uses the v2 format (Flow 29).
func isEnvelopeV2(envelope []byte) bool {
	return len(envelope) > len(envelopeV2Magic) &&
		bytes.Equal(envelope[:len(envelopeV2Magic)], envelopeV2Magic) &&
		envelope[len(envelopeV2Magic)] == envelopeV2Format
}

// sealEnvelopeV2 encrypts plaintext under a fresh DEK bound to storeKey (AES-GCM
// additional data) and wraps the DEK with the KEK of kekVersion. Layout:
// magic | 2 | kek_version | wrapped_dek_length | wrapped_dek | nonce || ciphertext.
func sealEnvelopeV2(keyEncryptionKey []byte, kekVersion int, storeKey string, plaintext []byte) ([]byte, error) {
	if kekVersion <= 0 || kekVersion > math.MaxUint32 {
		return nil, fmt.Errorf("seal v2 envelope: invalid KEK version %d", kekVersion)
	}
	dataEncryptionKey, generateError := GenerateDEK()
	if generateError != nil {
		return nil, generateError
	}
	sealedValue, encryptError := encryptWithKeyAndAdditionalData(dataEncryptionKey, plaintext, []byte(storeKey))
	if encryptError != nil {
		return nil, fmt.Errorf("seal v2 envelope: encrypt: %w", encryptError)
	}
	wrappedDEK, wrapError := encryptWithDEK(keyEncryptionKey, dataEncryptionKey)
	if wrapError != nil {
		return nil, fmt.Errorf("seal v2 envelope: wrap DEK: %w", wrapError)
	}
	return assembleEnvelopeV2(kekVersion, wrappedDEK, sealedValue), nil
}

// parseEnvelopeV2 splits a v2 envelope into its KEK version, wrapped DEK and
// sealed value. It does not decrypt anything.
func parseEnvelopeV2(envelope []byte) (int, []byte, []byte, error) {
	if !isEnvelopeV2(envelope) || len(envelope) < envelopeV2HeaderSize {
		return 0, nil, nil, errEnvelopeV2Malformed
	}
	versionOffset := len(envelopeV2Magic) + 1
	kekVersion := int(binary.BigEndian.Uint32(envelope[versionOffset : versionOffset+envelopeV2KEKVersionSize]))
	lengthOffset := versionOffset + envelopeV2KEKVersionSize
	wrappedDEKLength := int(binary.BigEndian.Uint16(envelope[lengthOffset : lengthOffset+envelopeV2WrappedDEKLengthSize]))
	if wrappedDEKLength == 0 || envelopeV2HeaderSize+wrappedDEKLength > len(envelope) {
		return 0, nil, nil, errEnvelopeV2Malformed
	}
	wrappedDEK := envelope[envelopeV2HeaderSize : envelopeV2HeaderSize+wrappedDEKLength]
	sealedValue := envelope[envelopeV2HeaderSize+wrappedDEKLength:]
	return kekVersion, wrappedDEK, sealedValue, nil
}

// rewrapEnvelopeV2 moves a v2 envelope to another KEK version: the DEK is
// unwrapped with currentKEK and wrapped with targetKEK. The nonce, ciphertext
// and AAD binding are unchanged; nothing is decrypted beyond the DEK (Flow 30).
func rewrapEnvelopeV2(envelope []byte, currentKEK []byte, targetKEK []byte, targetKEKVersion int) ([]byte, error) {
	if targetKEKVersion <= 0 || targetKEKVersion > math.MaxUint32 {
		return nil, fmt.Errorf("re-wrap v2 envelope: invalid KEK version %d", targetKEKVersion)
	}
	_, wrappedDEK, sealedValue, parseError := parseEnvelopeV2(envelope)
	if parseError != nil {
		return nil, parseError
	}
	dataEncryptionKey, unwrapError := decryptWithDEK(currentKEK, wrappedDEK)
	if unwrapError != nil {
		return nil, fmt.Errorf("re-wrap v2 envelope: unwrap DEK: %w", unwrapError)
	}
	rewrappedDEK, wrapError := encryptWithDEK(targetKEK, dataEncryptionKey)
	if wrapError != nil {
		return nil, fmt.Errorf("re-wrap v2 envelope: wrap DEK: %w", wrapError)
	}
	return assembleEnvelopeV2(targetKEKVersion, rewrappedDEK, sealedValue), nil
}

// assembleEnvelopeV2 lays out a v2 envelope; the caller checked kekVersion
// fits in 32 bits. Layout: magic | 2 | kek_version | wrapped_dek_length |
// wrapped_dek | nonce || ciphertext (sealedValue).
func assembleEnvelopeV2(kekVersion int, wrappedDEK []byte, sealedValue []byte) []byte {
	envelope := make([]byte, 0, envelopeV2HeaderSize+len(wrappedDEK)+len(sealedValue))
	envelope = append(envelope, envelopeV2Magic...)
	envelope = append(envelope, envelopeV2Format)
	envelope = binary.BigEndian.AppendUint32(envelope, uint32(kekVersion))      //nolint:gosec // range checked by the callers
	envelope = binary.BigEndian.AppendUint16(envelope, uint16(len(wrappedDEK))) //nolint:gosec // AES-GCM wrapped DEK is 60 bytes
	envelope = append(envelope, wrappedDEK...)
	return append(envelope, sealedValue...)
}

// openEnvelopeV2Payload unwraps the DEK with the KEK and decrypts the value;
// it fails when storeKey differs from the key the envelope was sealed for.
func openEnvelopeV2Payload(keyEncryptionKey []byte, wrappedDEK []byte, sealedValue []byte, storeKey string) ([]byte, error) {
	dataEncryptionKey, unwrapError := decryptWithDEK(keyEncryptionKey, wrappedDEK)
	if unwrapError != nil {
		return nil, fmt.Errorf("open v2 envelope: unwrap DEK: %w", unwrapError)
	}
	plaintext, decryptError := decryptWithKeyAndAdditionalData(dataEncryptionKey, sealedValue, []byte(storeKey))
	if decryptError != nil {
		return nil, fmt.Errorf("open v2 envelope for %s: %w", storeKey, decryptError)
	}
	return plaintext, nil
}
