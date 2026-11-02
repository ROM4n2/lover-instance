package api

import (
	"bytes"
	"net/http"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/demo"
	"lover-instance/backend-go/internal/ingest"
)

func demoHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		imported, err := ingest.Run(ctx, opts.Store, ingest.Options{
			Source: "chatlog-keeper",
			Talker: demo.SampleTalker,
			Reader: bytes.NewReader(demo.SampleChatJSON),
		})
		if err != nil {
			writeError(c, http.StatusInternalServerError, "demo_failed", "failed to import demo data", err.Error())
			return
		}

		if err := opts.Store.RebuildBehaviorMetrics(ctx, demo.SampleTalker); err != nil {
			writeError(c, http.StatusInternalServerError, "demo_failed", "failed to compute demo metrics", err.Error())
			return
		}

		ha := &analyze.HighlightsAnalyzer{}
		if err := ha.Run(ctx, opts.Store, analyze.Options{Talker: demo.SampleTalker}, nil); err != nil {
			writeError(c, http.StatusInternalServerError, "demo_failed", "failed to compute demo highlights", err.Error())
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"ok":       true,
			"talker":   demo.SampleTalker,
			"messages": imported.Inserted + imported.Skipped,
		})
	}
}
