// Package: pkg/security
// Purpose: Enterprise-grade JWT authentication
// Security: Implements proper JWT verification with expiry, signature validation

package security

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims represents JWT claims for StonkAgents
type Claims struct {
	PeerID string `json:"peer_id"`
	Role   string `json:"role"` // "daemon", "client", "admin"
	jwt.RegisteredClaims
}

// JWTAuthenticator handles JWT creation and validation
type JWTAuthenticator struct {
	secretKey []byte
	issuer    string
	audience  string
}

// NewJWTAuthenticator creates a new JWT authenticator
// secretKey should be loaded from environment variable, never hardcoded
func NewJWTAuthenticator() (*JWTAuthenticator, error) {
	// Load secret from environment variable (REQUIRED for security)
	secretKey := os.Getenv("JWT_SECRET")
	if secretKey == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable not set")
	}

	// Validate secret strength (minimum 32 characters)
	if len(secretKey) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}

	issuer := os.Getenv("JWT_ISSUER")
	if issuer == "" {
		issuer = "stonkagents" // Default issuer
	}

	audience := os.Getenv("JWT_AUDIENCE")
	if audience == "" {
		audience = "stonkagents-api" // Default audience
	}

	return &JWTAuthenticator{
		secretKey: []byte(secretKey),
		issuer:    issuer,
		audience:  audience,
	}, nil
}

// GenerateToken generates a new JWT token for a peer
func (j *JWTAuthenticator) GenerateToken(peerID, role string, expiryDuration time.Duration) (string, error) {
	// Validate inputs
	if peerID == "" {
		return "", fmt.Errorf("peerID cannot be empty")
	}
	if role == "" {
		role = "client" // Default role
	}

	// Validate role
	validRoles := map[string]bool{
		"daemon": true,
		"client": true,
		"admin":  true,
	}
	if !validRoles[role] {
		return "", fmt.Errorf("invalid role: %s", role)
	}

	now := time.Now()
	claims := &Claims{
		PeerID: peerID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.issuer,
			Audience:  jwt.ClaimStrings{j.audience},
			ExpiresAt: jwt.NewNumericDate(now.Add(expiryDuration)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        fmt.Sprintf("%s-%d", peerID, now.Unix()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(j.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenString, nil
}

// ValidateToken validates a JWT token and returns the claims
func (j *JWTAuthenticator) ValidateToken(tokenString string) (*Claims, error) {
	// Parse token with claims
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secretKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Extract claims
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Validate issuer
	if claims.Issuer != j.issuer {
		return nil, fmt.Errorf("invalid issuer: expected %s, got %s", j.issuer, claims.Issuer)
	}

	// Validate audience
	validAudience := false
	for _, aud := range claims.Audience {
		if aud == j.audience {
			validAudience = true
			break
		}
	}
	if !validAudience {
		return nil, fmt.Errorf("invalid audience: expected %s", j.audience)
	}

	// Validate expiry
	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("token expired")
	}

	// Validate not-before
	if claims.NotBefore != nil && claims.NotBefore.After(time.Now()) {
		return nil, fmt.Errorf("token not yet valid")
	}

	return claims, nil
}

// ValidateBearerToken extracts and validates a Bearer token from Authorization header
func (j *JWTAuthenticator) ValidateBearerToken(authHeader string) (*Claims, error) {
	// Check for "Bearer " prefix
	if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
		return nil, fmt.Errorf("invalid authorization header format")
	}

	// Extract token
	tokenString := authHeader[7:]
	if tokenString == "" {
		return nil, fmt.Errorf("empty bearer token")
	}

	// Validate token
	return j.ValidateToken(tokenString)
}

// HasRole checks if the claims have the required role
func (c *Claims) HasRole(requiredRole string) bool {
	return c.Role == requiredRole || c.Role == "admin" // Admin has all roles
}
