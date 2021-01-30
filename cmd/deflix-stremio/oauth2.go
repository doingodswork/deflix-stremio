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
// The handler returns a redirect to the RealDebrid or Premiumize OAuth2 *authorize* endpoint.
func createOAUTH2initHandler(confRD, confPM oauth2.Config, isHTTPS bool, logger *zap.Logger) fiber.Handler {
	confMap := map[string]oauth2.Config{
		"rd": confRD,
		"pm": confPM,
	}

	return func(c *fiber.Ctx) error {
		service := c.Params("service")
		if service == "" {
			return c.SendStatus(fiber.StatusBadRequest)
		} else if service != "rd" && service != "pm" {
			return c.SendStatus(fiber.StatusNotFound)
		}

		conf := confMap[service]

		// Create random state string
		randInt, err := crand.Int(crand.Reader, big.NewInt(6)) // 0-5
		if err != nil {
			logger.Error("Couldn't generate random number", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		statusLength := randInt.Add(randInt, big.NewInt(5)) // 5-10
		if !statusLength.IsUint64() {
			logger.Error("Random status length can't be represendted as uint64", zap.String("statusLength", statusLength.String()))
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
		redirectURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline)
		// Set as cookie, so when the redirect endpoint is hit we can make sure the state is the one we set in the user session
		statusCookie := &fiber.Cookie{
			Name:     "deflix_oauth2state",
			Value:    state,
			Secure:   isHTTPS,
			HTTPOnly: true,
			// We need the cookie to be sent upon redirect from RealDebrid or Premiumize to deflix-stremio.
			SameSite: "lax",
			// The cookie shouldn't be set forever
			MaxAge: 1 * 60 * 60, // One hour in seconds
		}
		c.Cookie(statusCookie)
		c.Set(fiber.HeaderLocation, redirectURL)
		return c.SendStatus(fiber.StatusTemporaryRedirect)
	}
}

// createOAUTH2installHandler returns a handler for redirected requests from RealDebrid or Premiumize after authorization.
// It returns something like the "/configure" page, but pre-filled with the required RealDebrid or Premiumize data.
// aesKey should be 32 bytes so that AES-256 is used.
func createOAUTH2installHandler(confRD, confPM oauth2.Config, aesKey []byte, logger *zap.Logger) fiber.Handler {
	confMap := map[string]oauth2.Config{
		"rd": confRD,
		"pm": confPM,
	}

	return func(c *fiber.Ctx) error {
		service := c.Params("service")
		if service == "" {
			return c.SendStatus(fiber.StatusBadRequest)
		} else if service != "rd" && service != "pm" {
			return c.SendStatus(fiber.StatusNotFound)
		}

		conf := confMap[service]

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
		token, err := conf.Exchange(c.Context(), code, oauth2.AccessTypeOffline)
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
		// The encoding below leads to double Base64 encoding, but using `string(ciphertext)` leads to much longer and uglier Base64-encoded user data.
		var ud userData
		if service == "rd" {
			ud = userData{
				RDoauth2: base64.RawURLEncoding.EncodeToString(ciphertext),
			}
		} else if service == "pm" {
			ud = userData{
				PMoauth2: base64.RawURLEncoding.EncodeToString(ciphertext),
			}
		}
		// else is taken care of at the start of the handler
		userDataEncoded, err := ud.encode(logger)
		if err != nil {
			logger.Error("Couldn't encode user data with OAuth2 data", zap.Error(err))
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		// If a redirect URL is set in a cookie, it could be from www.deflix.tv or from a promo page and we must redirect there instead of to our "/configure#..." page.
		redirectURL := "/configure#" + userDataEncoded
		if c.Cookies("deflix_oauth2redirect") != "" {
			redirectURL = c.Cookies("deflix_oauth2redirect")
			redirectURL += "?data=" + userDataEncoded
		}

		c.Set(fiber.HeaderLocation, redirectURL)
		return c.SendStatus(http.StatusTemporaryRedirect)
	}
}
