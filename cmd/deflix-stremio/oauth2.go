package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"math/big"

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
