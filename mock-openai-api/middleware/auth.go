package middleware

import (
	"fmt"
	"mock-openai-api/pkg/config"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 鉴权中间件，用来给你的 Mock OpenAI API 做访问权限校验。
func Auth() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		cnf := config.GetConfig()
		authorization := ctx.Request.Header.Get("Authorization")
		confAuthorization := fmt.Sprintf("Bearer %s", cnf.Http.AccessToken)
		if authorization != confAuthorization {
			ctx.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		ctx.Next()
	}
}
