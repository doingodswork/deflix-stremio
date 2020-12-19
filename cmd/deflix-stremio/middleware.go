package main

import (
	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/premiumize"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
)

// createTokenMiddleware creates a middleware that checks the validity of RealDebrid and AllDebrid API tokens/keys.
func createTokenMiddleware(rdClient *realdebrid.Client, adClient *alldebrid.Client, pmClient *premiumize.Client, logger *zap.Logger) fiber.Handler {
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

		if userData.RDtoken == "" && userData.ADkey == "" && userData.PMkey == "" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		// We expect a user to have *either* an RD token *or* an AD key, not both
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
		}

		// Note: We don't put the API token nor the remote info into the context,
		// because the stream handler doesn't have access to the fiber context
		// and we can't write to the underlying stdlib context here.

		return c.Next()
	}
}
