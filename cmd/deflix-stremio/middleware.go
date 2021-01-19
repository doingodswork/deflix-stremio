package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/deflix-tv/go-debrid/alldebrid"
	"github.com/deflix-tv/go-debrid/premiumize"
	"github.com/deflix-tv/go-debrid/realdebrid"
)

// createAuthMiddleware creates a middleware that checks the validity of RealDebrid, AllDebrid and Premiumize API tokens/keys as well as Premiumize OAuth2 data.
func createAuthMiddleware(rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, useOAUTH2 bool, confRD, confPM oauth2.Config, aesKey []byte, logger *zap.Logger) fiber.Handler {
	httpClient := &http.Client{
		Timeout: 2 * time.Second,
	}

	return func(c *fiber.Ctx) error {
		rCtx := c.Context()
		udString := c.Params("userData", "")
		if udString == "" {
			// Should never occur, because the manifest states that configuration is required and go-stremio's route matcher middleware filters these out.
			logger.Error("User data is empty, but this should have been handled by go-stremio's router matcher middleware alraedy")
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		userData, err := decodeUserData(udString, logger)
		if err != nil {
			// The error is already logged in the decodeUserData function.
			// It's most likely a client-side encoding error.
			return c.SendStatus(fiber.StatusBadRequest)
		}

		// Note: Even when useOAUTH2 is true, some Stremio clients might still use the API key from the past.
		if useOAUTH2 && (userData.RDoauth2 != "" || userData.PMoauth2 != "") {
			if userData.RDoauth2 != "" {
				accessToken, err, fiberErr := getAccessTokenForOAuth2data(c, confRD, aesKey, userData.RDoauth2, true, httpClient, logger)
				if err != nil {
					logger.Warn("Couldn't get access token for OAUTH2 data", zap.Error(err))
					// HTTP responses are already handled
					return fiberErr
				}
				if err = rdClient.TestToken(c.Context(), accessToken); err != nil {
					logger.Info("Access token is invalid or validation failed", zap.Error(err))
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", accessToken)
			} else if userData.PMoauth2 != "" {
				accessToken, err, fiberErr := getAccessTokenForOAuth2data(c, confPM, aesKey, userData.PMoauth2, false, nil, logger)
				if err != nil {
					logger.Warn("Couldn't get access token for OAUTH2 data", zap.Error(err))
					// HTTP responses are already handled
					return fiberErr
				}
				c.Locals("debrid_OAUTH2", struct{}{})
				if err = pmClient.TestAPIkey(c.Context(), accessToken); err != nil {
					logger.Info("Access token is invalid or validation failed", zap.Error(err))
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", accessToken)
			}
		} else {
			// Log "legacy" info. Only for RD and PM, because we're still using API keys for AD even if useOAUTH2 is true.
			if useOAUTH2 && (userData.RDtoken != "" || userData.PMkey != "") {
				logger.Info("Using OAUTH2, but a client used an API key")
			}
			// We expect a user to have *either* an RD token *or* an AD key *or* a Premiumize key
			if userData.RDtoken != "" {
				if err := rdClient.TestToken(rCtx, userData.RDtoken); err != nil {
					logger.Info("API key is invalid or validation failed", zap.Error(err))
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", userData.RDtoken)
			} else if userData.ADkey != "" {
				if err := adClient.TestAPIkey(rCtx, userData.ADkey); err != nil {
					logger.Info("API key is invalid or validation failed", zap.Error(err))
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", userData.ADkey)
			} else if userData.PMkey != "" {
				if err := pmClient.TestAPIkey(rCtx, userData.PMkey); err != nil {
					logger.Info("API key is invalid or validation failed", zap.Error(err))
					return c.SendStatus(fiber.StatusForbidden)
				}
				c.Locals("deflix_keyOrToken", userData.PMkey)
			} else {
				logger.Info("API key is empty", zap.String("userData", fmt.Sprintf("%+v", userData)))
				return c.SendStatus(fiber.StatusUnauthorized)
			}
		}

		return c.Next()
	}
}

// getAccessTokenForOAuth2data is a convenience function that decrypts the OAUTH2 data and returns a valid (potentially refreshed) access token,
// while taking care of Fiber responses in error cases.
// The first error return value is the error that occurred inside this function. The second is from sending the response via Fiber.
func getAccessTokenForOAuth2data(c *fiber.Ctx, conf oauth2.Config, aesKey []byte, oauth2data string, rdWorkaround bool, httpClient *http.Client, logger *zap.Logger) (string, error, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(oauth2data)
	if err != nil {
		// It's most likely a client-side encoding error
		return "", err, c.SendStatus(fiber.StatusBadRequest)
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		logger.Warn("Couldn't create block cipher from AES key", zap.Error(err))
		return "", err, c.SendStatus(fiber.StatusInternalServerError)
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		logger.Error("Couldn't create AES GCM", zap.Error(err))
		return "", err, c.SendStatus(fiber.StatusInternalServerError)
	}
	// The nonce is prepended
	nonce := ciphertext[:aesgcm.NonceSize()]
	ciphertext = ciphertext[aesgcm.NonceSize():]

	tokenJSON, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err, c.SendStatus(fiber.StatusForbidden)
	}
	token := &oauth2.Token{}
	if err = json.Unmarshal(tokenJSON, token); err != nil {
		// How likely is it that if the previous decoding worked, that it's now the client's fault vs ours?
		return "", err, c.SendStatus(fiber.StatusBadRequest)
	}
	// This is a workaround for RD, as they don't seem to implement the OAuth2 flow the way the Go OAuth2 package expects
	// (for example they require grant_type: "http://oauth.net/grant_type/device/1.0", instead of "refresh_token")
	var accessToken string
	if rdWorkaround {
		// Example call from RD docs:
		// curl -X POST "https://api.real-debrid.com/oauth/v2/token" -d "client_id=ABCDEFGHIJKLM&client_secret=abcdefghsecret0123456789&code=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789&grant_type=http://oauth.net/grant_type/device/1.0"
		data := url.Values{}
		data.Add("client_id", conf.ClientID)
		data.Add("client_secret", conf.ClientSecret)
		data.Add("code", token.RefreshToken)
		data.Add("grant_type", "http://oauth.net/grant_type/device/1.0")
		req, err := http.NewRequest("POST", conf.Endpoint.TokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			logger.Error("Couldn't create request object for RD token refresh", zap.Error(err))
			return "", err, c.SendStatus(fiber.StatusInternalServerError)
		}
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationForm)
		res, err := httpClient.Do(req)
		if err != nil {
			logger.Warn("Error during request to RD token refresh", zap.Error(err))
			return "", err, c.SendStatus(fiber.StatusInternalServerError)
		}
		defer res.Body.Close()
		// RD API usually always responds with 200 and a JSON object (even for bad requests / invalid accounts etc),
		// with the actual potential error codes are then inside the JSON body,
		// but in case of the OAuth2 refresh token request this is different.
		// So we have to treat non-OK responses as errors from the client side, not from us.
		if res.StatusCode != fiber.StatusOK {
			var errBody []byte
			errBody, _ = ioutil.ReadAll(res.Body)
			logger.Info("RD token refresh response != OK", zap.Int("status", res.StatusCode), zap.ByteString("body", errBody))
			return "", errors.New("RD response != OK"), c.SendStatus(fiber.StatusForbidden)
		}
		tokenJSON, err = ioutil.ReadAll(res.Body)
		if err != nil {
			logger.Warn("Couldn't read response body from RD token refresh", zap.Error(err))
			return "", err, c.SendStatus(fiber.StatusInternalServerError)
		}
		if err = json.Unmarshal(tokenJSON, token); err != nil {
			logger.Warn("Couldn't unmarshal RD response body into OAuth2 token", zap.Error(err), zap.ByteString("body", tokenJSON))
			return "", err, c.SendStatus(fiber.StatusInternalServerError)
		}
		accessToken = token.AccessToken
	} else {
		tokenSource := conf.TokenSource(c.Context(), token)
		// The token source automatically refreshes the token with the refresh token
		validToken, err := tokenSource.Token()
		if err != nil {
			return "", err, c.SendStatus(fiber.StatusForbidden)
		}
		accessToken = validToken.AccessToken
	}

	return accessToken, nil, nil
}
