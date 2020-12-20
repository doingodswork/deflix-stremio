package main

import (
	crand "crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
)

// oauth2ConfigPM is the OAuth2 config for Premiumize.
type oauth2ConfigPM struct {
	// For the initial authorization request
	AuthorizeURL string
	ClientID     string
	// For the subsequent token request
	// ClientSecret string
}

// createOAUTH2initHandler returns a handler for OAuth2 initialization requests from the deflix-stremio frontend.
// The handler returns a redirect to the Premiumize OAuth2 *authorize* endpoint.
func createOAUTH2initHandler(confPM oauth2ConfigPM, logger *zap.Logger) fiber.Handler {
	// Example:
	//
	// https://authorization-server.com/authorize?
	// response_type=code
	// &client_id=gtsiL1dcGyORX1JFGIa98hNf
	// &redirect_uri=https://www.oauth.com/playground/authorization-code.html
	// &scope=photo+offline_access
	// &state=1bV05WlyyT8dxdb1
	//
	// Redirect URL and scope are optional.
	redirectURL := confPM.AuthorizeURL + "?response_type=code&client_id=" + confPM.ClientID + "&state=%v"
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
		_, _ = crand.Read(b)
		// URL-safe, no padding
		state := base64.RawURLEncoding.EncodeToString(b)

		// Create redirect URL with random state string
		redirectURL = fmt.Sprintf(redirectURL, state)
		// Set as cookie, so when the redirect endpoint is hit we can make sure the state is the one we set in the user session
		statusCookie := &fiber.Cookie{
			Name:     "deflix_oauth2state",
			Value:    state,
			Secure:   true,
			HTTPOnly: true,
			// We need the cookie to be sent upon redirect
			SameSite: "lax",
		}
		c.Cookie(statusCookie)
		c.Set(fiber.HeaderLocation, redirectURL)
		return c.SendStatus(fiber.StatusTemporaryRedirect)
	}
}
