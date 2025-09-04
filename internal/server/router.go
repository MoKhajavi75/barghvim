package server

import (
	"net/http"
	"time"

	"github.com/MoKhajavi75/barghvim/internal/calendar"
	"github.com/MoKhajavi75/barghvim/internal/outages"
	"github.com/gin-gonic/gin"
)

func New() *gin.Engine {
	r := gin.Default()

	v1 := r.Group("/v1")
	{
		v1.GET("/:bill/cal.ics", func(ctx *gin.Context) {
			bill := ctx.Param("bill")
			token := ctx.Query("token")

			if bill == "" {
				ctx.String(http.StatusBadRequest, "missing bill")
				return
			}

			if token == "" {
				ctx.String(http.StatusBadRequest, "missing token")
				return
			}

			outages, err := outages.Fetch(ctx.Request.Context(), token, bill)
			if err != nil {
				ctx.String(http.StatusInternalServerError, "upstream error")
				return
			}

			icsBytes, err := calendar.BuildICS(bill, outages)
			if err != nil {
				ctx.String(http.StatusInternalServerError, "calendar error")
				return
			}

			ctx.Header("Content-Type", "text/calendar; charset=utf-8")
			ctx.Header("Cache-Control", "no-store")
			ctx.Header("Pragma", "no-cache")
			ctx.Header("Expires", time.Unix(0, 0).UTC().Format(http.TimeFormat))
			ctx.Data(http.StatusOK, "text/calendar; charset=utf-8", icsBytes)
		})
	}

	return r
}
