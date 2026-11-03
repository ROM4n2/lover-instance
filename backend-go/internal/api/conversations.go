package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"lover-instance/backend-go/internal/app"
	"lover-instance/backend-go/internal/store"
)

// conversationsHandler returns all conversations in the store ordered by message count.
func conversationsHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Backfill from exports if contacts were not populated
		_ = opts.Store.BackfillFromExports(ctx, app.ExportsDir())

		list, err := opts.Store.ListConversations(ctx)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "store_error", "failed to list conversations", err.Error())
			return
		}
		if list == nil {
			list = []store.ConversationSummary{}
		}
		c.JSON(http.StatusOK, gin.H{
			"conversations": list,
		})
	}
}
