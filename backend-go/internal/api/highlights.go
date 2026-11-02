package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/report"
)

func highlightsHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		talker := c.Query("talker")
		if talker == "" {
			writeError(c, http.StatusBadRequest, "missing_talker", "talker query parameter is required", "pass ?talker=<wxid>")
			return
		}

		view, err := report.CollectHighlights(c.Request.Context(), opts.Store, talker)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "highlights_failed", "failed to collect highlights", err.Error())
			return
		}

		c.JSON(http.StatusOK, view)
	}
}
