// Copyright (C) 2026 CCattler Contributors
// SPDX-License-Identifier: GPL-3.0-only

package types

import "fmt"

// --- Route keys ---

// KeyDesiredRoute returns the store path for a route's root marker key.
// Path: desired/route/{name}
func KeyDesiredRoute(routeName string) string {
	return fmt.Sprintf("%s/route/%s", PrefixDesired, routeName)
}

// KeyDesiredRouteHost returns the store path for a route's hostname match.
// Path: desired/route/{name}/host
func KeyDesiredRouteHost(routeName string) string {
	return fmt.Sprintf("%s/route/%s/host", PrefixDesired, routeName)
}

// KeyDesiredRoutePath returns the store path for a route's URL path prefix match.
// Path: desired/route/{name}/path
func KeyDesiredRoutePath(routeName string) string {
	return fmt.Sprintf("%s/route/%s/path", PrefixDesired, routeName)
}

// KeyDesiredRouteService returns the store path for a route's target service name.
// Path: desired/route/{name}/service
func KeyDesiredRouteService(routeName string) string {
	return fmt.Sprintf("%s/route/%s/service", PrefixDesired, routeName)
}

// KeyDesiredRoutePort returns the store path for a route's target service port.
// Path: desired/route/{name}/port
func KeyDesiredRoutePort(routeName string) string {
	return fmt.Sprintf("%s/route/%s/port", PrefixDesired, routeName)
}

// KeyDesiredRouteSticky returns the store path for a route's sticky session method.
// Value is "cookie" or "source-ip".
// Path: desired/route/{name}/sticky
func KeyDesiredRouteSticky(routeName string) string {
	return fmt.Sprintf("%s/route/%s/sticky", PrefixDesired, routeName)
}

// KeyDesiredRouteCookieName returns the store path for a route's sticky cookie name.
// Path: desired/route/{name}/cookie/name
func KeyDesiredRouteCookieName(routeName string) string {
	return fmt.Sprintf("%s/route/%s/cookie/name", PrefixDesired, routeName)
}

// KeyDesiredRouteCookieSecure returns the store path for a route's sticky cookie Secure flag.
// Path: desired/route/{name}/cookie/secure
func KeyDesiredRouteCookieSecure(routeName string) string {
	return fmt.Sprintf("%s/route/%s/cookie/secure", PrefixDesired, routeName)
}

// KeyDesiredRouteCookieHTTPOnly returns the store path for a route's sticky cookie HttpOnly flag.
// Path: desired/route/{name}/cookie/httponly
func KeyDesiredRouteCookieHTTPOnly(routeName string) string {
	return fmt.Sprintf("%s/route/%s/cookie/httponly", PrefixDesired, routeName)
}

// KeyDesiredRouteCookieSameSite returns the store path for a route's sticky cookie SameSite attribute.
// Path: desired/route/{name}/cookie/samesite
func KeyDesiredRouteCookieSameSite(routeName string) string {
	return fmt.Sprintf("%s/route/%s/cookie/samesite", PrefixDesired, routeName)
}

// KeyDesiredRouteTLSMode returns the store path for a route's TLS termination mode.
// Value is "auto" (cluster CA issues cert) or "manual" (user-provided PEM).
// Path: desired/route/{name}/tls/mode
func KeyDesiredRouteTLSMode(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/mode", PrefixDesired, routeName)
}

// KeyDesiredRouteTLSCert returns the store path for a route's TLS certificate PEM.
// Path: desired/route/{name}/tls/cert
func KeyDesiredRouteTLSCert(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/cert", PrefixDesired, routeName)
}

// KeyDesiredRouteTLSKey returns the store path for a route's TLS private key PEM.
// Path: desired/route/{name}/tls/key
func KeyDesiredRouteTLSKey(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/key", PrefixDesired, routeName)
}

// ScanDesiredRoutes is the prefix for scanning all route definitions.
const ScanDesiredRoutes = PrefixDesired + "/route/"

// KeyDerivedRouteBackends returns the store path for a route's resolved backend
// list. Value is a comma-separated list of host:port addresses.
// Path: derived/route/{name}/backends
func KeyDerivedRouteBackends(routeName string) string {
	return fmt.Sprintf("%s/route/%s/backends", PrefixDerived, routeName)
}

// KeyDerivedRouteTLSCert returns the store path for a route's CA-issued TLS cert.
// Path: derived/route/{name}/tls/cert
func KeyDerivedRouteTLSCert(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/cert", PrefixDerived, routeName)
}

// KeyDerivedRouteTLSKey returns the store path for a route's CA-issued TLS key.
// Path: derived/route/{name}/tls/key
func KeyDerivedRouteTLSKey(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/key", PrefixDerived, routeName)
}

// KeyDerivedRouteTLSNotAfter returns the store path for a route's TLS cert expiry.
// Path: derived/route/{name}/tls/not-after
func KeyDerivedRouteTLSNotAfter(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/not-after", PrefixDerived, routeName)
}

// ScanDerivedRoutes is the prefix for scanning all derived route facts.
const ScanDerivedRoutes = PrefixDerived + "/route/"

// SecretNameRouteTLSKeyPrefix is the secret-name prefix of system-owned gate
// TLS private keys (secrets/route/{gate}/tls/key), written by the route-tls
// controller and the gate-key API, never by `cca secret set` (M176).
const SecretNameRouteTLSKeyPrefix = "route/"

// SecretNameRouteTLSKey returns the secret name (relative to secrets/) of a
// gate's system-owned TLS private key envelope.
// Path: secrets/route/{gate}/tls/key
func SecretNameRouteTLSKey(routeName string) string {
	return SecretNameRouteTLSKeyPrefix + routeName + "/tls/key"
}

// KeyDesiredRouteTLSKeySecret returns the store path naming the user secret
// that holds a manual gate's private key (`tls { key secret "…" }`).
// Path: desired/route/{name}/tls/key_secret
func KeyDesiredRouteTLSKeySecret(routeName string) string {
	return fmt.Sprintf("%s/route/%s/tls/key_secret", PrefixDesired, routeName)
}
