package t3relay

import (
	"crypto/subtle"
)

// validateBearer returns true if token is one of the configured tokens.
// Uses constant-time comparison to prevent timing attacks.
func validateBearer(tokens []string, token string) bool {
	if token == "" || len(tokens) == 0 {
		return false
	}
	tokenBytes := []byte(token)
	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(t), tokenBytes) == 1 {
			return true
		}
	}
	return false
}

// extractBearer extracts the token from an Authorization: Bearer <token> header value.
// Returns empty string if the header is not in the expected format.
func extractBearer(authHeader string) string {
	const prefix = "Bearer "
	if len(authHeader) <= len(prefix) {
		return ""
	}
	if authHeader[:len(prefix)] != prefix {
		return ""
	}
	return authHeader[len(prefix):]
}
