package main

import (
	"context"
	"strings"

	"github.com/gofiber/fiber"
	"go.uber.org/zap"

	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
)

// createTokenMiddleware creates a middleware that checks the validity of RealDebrid API tokens.
func createTokenMiddleware(ctx context.Context, conversionClient realdebrid.Client, logger *zap.Logger) fiber.Handler {
	return func(c *fiber.Ctx) {
		rCtx := c.Context()
		apiToken := c.Params("userData", "")
		if apiToken == "" {
			c.SendStatus(fiber.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(apiToken, "-remote") {
			apiToken = strings.TrimSuffix(apiToken, "-remote")
		}
		if err := conversionClient.TestToken(rCtx, apiToken); err != nil {
			c.SendStatus(fiber.StatusForbidden)
			return
		}

		// Note: We don't put the API token nor the remote info into the context,
		// because the stream handler doesn't have access to the fiber context
		// and we can't write to the underlying stdlib context here.

		c.Next()
	}
}
