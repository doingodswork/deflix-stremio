package main

import (
	"github.com/gofiber/fiber"
	"go.uber.org/zap"

	"github.com/doingodswork/deflix-stremio/pkg/debrid/alldebrid"
	"github.com/doingodswork/deflix-stremio/pkg/debrid/realdebrid"
)

// createTokenMiddleware creates a middleware that checks the validity of RealDebrid and AllDebrid API tokens/keys.
func createTokenMiddleware(rdClient *realdebrid.Client, adClient *alldebrid.Client, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) {
		rCtx := c.Context()
		udString := c.Params("userData", "")
		if udString == "" {
			c.SendStatus(fiber.StatusUnauthorized)
			return
		}
		userData, err := decodeUserData(udString, logger)
		if err != nil {
			// It's most likely a client-side encoding error
			c.SendStatus(fiber.StatusBadRequest)
			// The error is already logged by decodeUserData
			return
		}

		if userData.RDtoken == "" && userData.ADkey == "" {
			c.SendStatus(fiber.StatusUnauthorized)
			return
		}
		// We expect a user to have *either* an RD token *or* an AD key, not both
		if userData.RDtoken != "" {
			if err := rdClient.TestToken(rCtx, userData.RDtoken); err != nil {
				c.SendStatus(fiber.StatusForbidden)
				return
			}
		} else if userData.ADkey != "" {
			if err := adClient.TestAPIkey(rCtx, userData.ADkey); err != nil {
				c.SendStatus(fiber.StatusForbidden)
				return
			}
		}

		// Note: We don't put the API token nor the remote info into the context,
		// because the stream handler doesn't have access to the fiber context
		// and we can't write to the underlying stdlib context here.

		c.Next()
	}
}
