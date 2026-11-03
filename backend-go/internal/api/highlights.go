package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/report"
)

func highlightsHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		talker, failure := resolveTalker(ctx, c.Query("talker"), opts.Store)
		if failure != nil {
			failure.write(c)
			return
		}

		view, err := report.CollectHighlights(ctx, opts.Store, talker)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "highlights_failed", "failed to collect highlights", err.Error())
			return
		}

		c.JSON(http.StatusOK, view)
	}
}
