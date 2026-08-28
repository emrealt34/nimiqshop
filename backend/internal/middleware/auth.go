package middleware

import (
	"strings"

	"github.com/valyala/fasthttp"

	"nimiqshop/internal/auth"
)

const userIDContextKey = "user_id"

// RequireAuth validates the Bearer JWT and stashes the user id on the
// fasthttp.RequestCtx for downstream handlers to read via UserID(ctx).
func RequireAuth(secret string, next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		header := string(ctx.Request.Header.Peek("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			ctx.Error(`{"error":"missing bearer token"}`, fasthttp.StatusUnauthorized)
			return
		}
		raw := strings.TrimPrefix(header, "Bearer ")

		claims, err := auth.ParseToken(secret, raw)
		if err != nil {
			ctx.Error(`{"error":"invalid or expired token"}`, fasthttp.StatusUnauthorized)
			return
		}

		ctx.SetUserValue(userIDContextKey, claims.UserID)
		next(ctx)
	}
}

func UserID(ctx *fasthttp.RequestCtx) string {
	v := ctx.UserValue(userIDContextKey)
	if v == nil {
		return ""
	}
	return v.(string)
}
