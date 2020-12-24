package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/premiumize"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
)

// createAuthMiddleware creates a middleware that checks the validity of RealDebrid, AllDebrid and Premiumize API tokens/keys as well as Premiumize OAuth2 data.
func createAuthMiddleware(rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, useOAUTH2 bool, confPM oauth2.Config, aesKey []byte, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		rCtx := c.Context()
		udString := c.Params("userData", "")
		if udString == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		userData, err := decodeUserData(udString, logger)
		if err != nil {
			// It's most likely a client-side encoding error
			return c.SendStatus(fiber.StatusBadRequest)
			// The error is already logged by decodeUserData
		}

		if useOAUTH2 {
			if userData.RDtoken == "" && userData.ADkey == "" && userData.PMoauth2 == "" {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			// We expect a user to have *either* an RD token *or* an AD key *or* Premiumize OAuth2 data
			if userData.RDtoken != "" {
				if err := rdClient.TestToken(rCtx, userData.RDtoken); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
			} else if userData.ADkey != "" {
				if err := adClient.TestAPIkey(rCtx, userData.ADkey); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
			} else if userData.PMoauth2 != "" {
				ciphertext, err := base64.RawURLEncoding.DecodeString(userData.PMoauth2)
				if err != nil {
					// It's most likely a client-side encoding error
					return c.SendStatus(fiber.StatusBadRequest)
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
				// The nonce is prepended
				nonce := ciphertext[:aesgcm.NonceSize()]
				ciphertext = ciphertext[aesgcm.NonceSize():]

				tokenJSON, err := aesgcm.Open(nil, nonce, ciphertext, nil)
				if err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
				token := &oauth2.Token{}
				if err = json.Unmarshal(tokenJSON, token); err != nil {
					// How likely is it that if the previous decoding worked, that it's now the client's fault vs ours?
					return c.SendStatus(fiber.StatusBadRequest)
				}
				tokenSource := confPM.TokenSource(c.Context(), token)
				// The token source automatically refreshes the token with the refresh token
				validToken, err := tokenSource.Token()
				if err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
				accessToken := validToken.AccessToken
				if err = pmClient.TestAPIkey(rCtx, accessToken); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", accessToken)
			}
		} else {
			if userData.RDtoken == "" && userData.ADkey == "" && userData.PMkey == "" {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			// We expect a user to have *either* an RD token *or* an AD key *or* a Premiumize key
			if userData.RDtoken != "" {
				if err := rdClient.TestToken(rCtx, userData.RDtoken); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
			} else if userData.ADkey != "" {
				if err := adClient.TestAPIkey(rCtx, userData.ADkey); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
			} else if userData.PMkey != "" {
				if err := pmClient.TestAPIkey(rCtx, userData.PMkey); err != nil {
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", userData.PMkey)
			}
		}

		return c.Next()
	}
}
