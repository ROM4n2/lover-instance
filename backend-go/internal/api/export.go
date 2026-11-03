package api

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/report"
	"lover-instance/backend-go/internal/store"
)

const (
	// exportsDirPerm and exportFilePerm are owner-only. The file is the
	// conversation's aggregate view, but it still names the pair's habits, and
	// on a shared Windows account "the other user can open it" is not a
	// property this tool should have.
	exportsDirPerm   = 0o700
	exportFilePerm   = 0o600
	exportFileSuffix = ".html"
)

// externalRefs is report.ExternalRefs behind a seam.
//
// The guard is the one the CLI applies before it writes a report, and the only
// way to test that this endpoint honours it is to make the guard fire without a
// view that happens to contain a URL. Nothing in production replaces it.
var externalRefs = report.ExternalRefs

// exportRequest is the body of POST /api/export.
type exportRequest struct {
	Talker       string `json:"talker"`
	ModelVersion string `json:"model_version"`
}

// exportResponse names the file that was written and how big it is.
//
// The document itself is deliberately not in this response. It is roughly a
// megabyte of HTML with two megabytes of inlined chart library, and shipping
// that through an API call so the browser can hand it straight back to the disk
// is a copy for nothing; the browser opens the path instead.
type exportResponse struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

// exportHandler answers POST /api/export by rendering the report to a file.
func exportHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request exportRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeError(c, http.StatusBadRequest, "invalid_body",
				"the request body is not the JSON object this endpoint expects",
				`send {"talker":"<fingerprint or conversation key>","model_version":"<optional>"}`)
			return
		}
		ctx := c.Request.Context()

		talker, failure := resolveTalker(ctx, request.Talker, opts.Store)
		if failure != nil {
			failure.write(c)
			return
		}
		version, failure := sentimentVersion(ctx, opts, request.ModelVersion, talker)
		if failure != nil {
			failure.write(c)
			return
		}

		view, err := report.Collect(ctx, opts.Store, talker, store.BehaviorModelVersion, version, nil)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the report could not be assembled from this database",
				"retry the request; if it keeps failing, check /api/health and the server's log")
			return
		}
		var document bytes.Buffer
		if err := report.Render(&document, view); err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the report could not be rendered",
				"retry the request; if it keeps failing, check the server's log")
			return
		}

		// The same guard the CLI applies, before a byte is written: a report
		// that reaches the network is not the self-contained file ADR-0003
		// promises, and a file on disk is much harder to un-share than a
		// refused request.
		if refs := externalRefs(document.Bytes()); len(refs) != 0 {
			writeError(c, http.StatusInternalServerError, "external_reference",
				"the rendered report would reach the network, which this tool forbids, so it was not written",
				fmt.Sprintf("the renderer produced %d external reference(s); this is a bug in the report template, not in your data", len(refs)))
			return
		}

		dir, err := exportDir(opts)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the export directory could not be located",
				"set LOCALAPPDATA, or start the server where a user profile exists")
			return
		}
		if err := os.MkdirAll(dir, exportsDirPerm); err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the export directory could not be created",
				"check that the process may write under LOCALAPPDATA and retry")
			return
		}

		// The file name is the fingerprint and a timestamp, never the talker:
		// a directory listing is a place a wxid would survive for a long time,
		// and an export is a file people attach to messages.
		path := filepath.Join(dir, report.TalkerHash(talker)+"-"+strconv.FormatInt(time.Now().Unix(), 10)+exportFileSuffix)
		if err := writeAtomically(path, document.Bytes()); err != nil {
			writeError(c, http.StatusInternalServerError, "internal",
				"the report was rendered but could not be written",
				"check the free space and the permissions of the export directory, then retry")
			return
		}

		c.JSON(http.StatusOK, exportResponse{Path: path, Bytes: document.Len()})
	}
}

// exportDir is where exported reports go: the location the design fixes, or the
// one the caller named (which is what a test does).
func exportDir(opts Options) (string, error) {
	if opts.ExportsDir != "" {
		return opts.ExportsDir, nil
	}
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate the export directory: %w", err)
		}
		root = dir
	}
	return filepath.Join(root, "lover-instance", "exports"), nil
}

// writeAtomically renders next to the final name and renames it into place.
//
// It is the same shape as the CLI's writeReport, and for the same reason: an
// interrupted write must not leave a truncated file that looks like a finished
// report. The temporary name shares the final directory so the rename stays on
// one volume.
func writeAtomically(path string, document []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, document, exportFilePerm); err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		// The half-written file is removed rather than left behind: a .tmp that
		// outlives its failed rename is a file nobody will ever look at again.
		_ = os.Remove(temporary)
		return fmt.Errorf("move export into place: %w", err)
	}
	return nil
}
