package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/ingest"
)

func importHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Enforce maximum body size 100MB
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 100<<20)

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid_file", "missing or unreadable file in multipart form", "upload via field 'file'")
			return
		}
		defer file.Close()

		if !strings.HasSuffix(strings.ToLower(header.Filename), ".json") {
			writeError(c, http.StatusBadRequest, "invalid_format", "only .json chatlog-keeper export files are accepted", "provide a valid JSON export")
			return
		}

		ctx := c.Request.Context()
		imported, err := ingest.Run(ctx, opts.Store, ingest.Options{
			Source: "chatlog-keeper",
			Reader: file,
		})
		if err != nil {
			writeError(c, http.StatusBadRequest, "import_failed", "could not parse chat export", err.Error())
			return
		}

		// Rebuild metrics and highlights for each touched conversation
		ha := &analyze.HighlightsAnalyzer{}
		for _, talker := range imported.Talkers {
			_ = opts.Store.RebuildBehaviorMetrics(ctx, talker)
			_ = ha.Run(ctx, opts.Store, analyze.Options{Talker: talker}, nil)
		}

		c.JSON(http.StatusOK, gin.H{
			"ok":       true,
			"talkers":  imported.Talkers,
			"inserted": imported.Inserted,
			"parsed":   imported.Parsed,
			"skipped":  imported.Skipped,
		})
	}
}
