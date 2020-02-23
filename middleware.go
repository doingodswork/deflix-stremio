package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"

	"github.com/doingodswork/deflix-stremio/pkg/realdebrid"
)

func createTimerMiddleware(ctx context.Context) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rCtx := r.Context()
			newReq := r.WithContext(context.WithValue(rCtx, "start", time.Now()))
			// Call the next handler, which can be another middleware in the chain, or the final handler.
			next.ServeHTTP(w, newReq)
		})
	}
}

func createCorsMiddleware(ctx context.Context) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Headers as listed by the Stremio example addon.
		//
		// According to logs an actual stream request sends these headers though:
		//   Header:map[
		// 	  Accept:[*/*]
		// 	  Accept-Encoding:[gzip, deflate, br]
		// 	  Connection:[keep-alive]
		// 	  Origin:[https://app.strem.io]
		// 	  User-Agent:[Mozilla/5.0 (Windows NT 6.2; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) QtWebEngine/5.9.9 Chrome/56.0.2924.122 Safari/537.36 StremioShell/4.4.106]
		// ]
		headersOk := handlers.AllowedHeaders([]string{
			"Accept",
			"Accept-Language",
			"Content-Type",
			"Origin", // Not "safelisted" in the specification

			// Non-default for gorilla/handlers CORS handling
			"Accept-Encoding",
			"Content-Language", // "Safelisted" in the specification
			"X-Requested-With",
		})
		originsOk := handlers.AllowedOrigins([]string{"*"})
		methodsOk := handlers.AllowedMethods([]string{"GET"})
		return handlers.CORS(originsOk, headersOk, methodsOk)(next)
	}
}

var recoveryMiddleware = handlers.RecoveryHandler(handlers.PrintRecoveryStack(true))

func createTokenMiddleware(ctx context.Context, conversionClient realdebrid.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rCtx := r.Context()
			params := mux.Vars(r)
			apiToken := params["apitoken"]
			if apiToken == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			remote := false
			if strings.HasSuffix(apiToken, "-remote") {
				remote = true
				apiToken = strings.TrimSuffix(apiToken, "-remote")
			}
			if err := conversionClient.TestToken(rCtx, apiToken); err != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}

			rCtx = context.WithValue(rCtx, "apitoken", apiToken)
			rCtx = context.WithValue(rCtx, "remote", remote)
			newReq := r.WithContext(rCtx)
			next.ServeHTTP(w, newReq)
		})
	}
}

func createLoggingMiddleware(ctx context.Context) func(http.Handler) http.Handler {
	return func(before http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rCtx := r.Context()
			// First call the *before* handler!
			before.ServeHTTP(w, r)
			// Then log
			reqStart := rCtx.Value("start").(time.Time)
			duration := time.Since(reqStart).Milliseconds()
			log.Println(r.Method, r.URL, "from", r.RemoteAddr, "took", duration, "ms")
		})
	}
}
