package main

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

// createOAUTH2initHandler returns a handler for OAuth2 initialization requests from the deflix-stremio frontend.
// The handler returns a redirect to the Premiumize OAuth2 *authorize* endpoint.
func createOAUTH2initHandler(confPM oauth2.Config, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Create random state string
		randInt, err := crand.Int(crand.Reader, big.NewInt(6)) // 0-5
		if err != nil {
			logger.Error("Couldn't generate random number", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		statusLength := randInt.Add(randInt, big.NewInt(5)) // 5-10
		if !statusLength.IsUint64() {
			logger.Error("Random status length can't be represendted as uint64", zap.Any("statusLength", statusLength))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		b := make([]byte, statusLength.Uint64())
		if _, err = crand.Read(b); err != nil {
			logger.Error("Couldn't generate random bytes", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		// URL-safe, no padding
		state := base64.RawURLEncoding.EncodeToString(b)

		// Create redirect URL with random state string
		redirectURL := confPM.AuthCodeURL(state, oauth2.AccessTypeOffline)
		// Set as cookie, so when the redirect endpoint is hit we can make sure the state is the one we set in the user session
		statusCookie := &fiber.Cookie{
			Name:     "deflix_oauth2state",
			Value:    state,
			Secure:   true,
			HTTPOnly: true,
			// We need the cookie to be sent upon redirect from Premiumize to deflix-stremio.
			SameSite: "lax",
		}
		c.Cookie(statusCookie)
		c.Set(fiber.HeaderLocation, redirectURL)
		return c.SendStatus(fiber.StatusTemporaryRedirect)
	}
}

// createOAUTH2installHandler returns a handler for redirected requests from Premiumize after authorization.
// It returns something like the "/configure" page, but pre-filled with the required Premiumize data.
// aesKey should be 32 bytes so that AES-256 is used.
func createOAUTH2installHandler(confPM oauth2.Config, aesKey []byte, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Verify state
		stateFromURL := c.Query("state")
		stateFromCookie := c.Cookies("deflix_oauth2state")
		if stateFromURL == "" || stateFromURL != stateFromCookie {
			return c.SendStatus(fiber.StatusForbidden)
		}

		// Exchange authorization code for access token
		code := c.Query("code")
		if code == "" {
			return c.SendStatus(fiber.StatusForbidden)
		}
		token, err := confPM.Exchange(c.Context(), code, oauth2.AccessTypeOffline)
		if err != nil {
			// Can be both client-side errors (e.g. faked code) or ours.
			logger.Warn("Couldn't exchange authorization code for access token", zap.Error(err))
			return c.SendStatus(fiber.StatusForbidden)
		}

		// Encrypt token so we can deliver it to the Stremio client without revealing the tokens.
		// We do this so we don't have to store it server-side, which is 1. error-prone (makes DB a single point of failure) and 2. a liability (DB hacks, leaks).
		tokenJSON, err := json.Marshal(token)
		if err != nil {
			logger.Error("Couldn't marshal the token into JSON", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		block, err := aes.NewCipher(aesKey)
		if err != nil {
			logger.Warn("Couldn't create block cipher from AES key", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		aesgcm, err := cipher.NewGCM(block)
		if err != nil {
			logger.Error("Couldn't create AES GCM", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		nonce := make([]byte, aesgcm.NonceSize())
		if _, err = crand.Read(nonce); err != nil {
			logger.Error("Couldn't create nonce", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		// We prepend the nonce because we don't want to store it
		ciphertext := aesgcm.Seal(nonce, nonce, tokenJSON, nil)

		// Redirect to the "/configure" webpage, but with the OAuth2 data in the URL so that the site's JavaScript can read and use it.
		userData := userData{
			// This leads to double Base64 encoding, but using `string(ciphertext)` leads to much longer and uglier Base64-encoded user data.
			PMoauth2: base64.RawURLEncoding.EncodeToString(ciphertext),
		}
		userDataEncoded, err := userData.encode(logger)
		if err != nil {
			logger.Error("Couldn't encode user data with OAuth2 data", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		c.Set(fiber.HeaderLocation, "/configure#"+userDataEncoded)
		return c.SendStatus(http.StatusTemporaryRedirect)
	}
}
