package api

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"lover-instance/backend-go/internal/analyze"
	"lover-instance/backend-go/internal/app"
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

func scanExportHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		candidates := make([]string, 0)
		if opts.ExportsDir != "" {
			candidates = append(candidates, opts.ExportsDir)
		} else {
			candidates = append(candidates, app.ExportsDir())
		}

		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Downloads"))
		}
		candidates = append(candidates, ".")

		var newestPath string
		var newestMod time.Time
		var newestSize int64

		for _, dir := range candidates {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			var checkPaths []string
			for _, entry := range entries {
				full := filepath.Join(dir, entry.Name())
				if entry.IsDir() {
					subEntries, subErr := os.ReadDir(full)
					if subErr == nil {
						for _, sub := range subEntries {
							if !sub.IsDir() && strings.HasSuffix(strings.ToLower(sub.Name()), ".json") {
								checkPaths = append(checkPaths, filepath.Join(full, sub.Name()))
							}
						}
					}
				} else if strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
					checkPaths = append(checkPaths, full)
				}
			}

			for _, p := range checkPaths {
				info, err := os.Stat(p)
				if err != nil || info.Size() < 50 {
					continue
				}
				if isCandidateExport(p) {
					if info.ModTime().After(newestMod) {
						newestMod = info.ModTime()
						newestPath = p
						newestSize = info.Size()
					}
				}
			}
		}

		if newestPath == "" {
			c.JSON(http.StatusOK, gin.H{
				"found":   false,
				"message": "未在下载目录或常用导出路径发现有效聊天记录 JSON 文件",
			})
			return
		}

		shouldImport := c.Query("import") == "true"
		if !shouldImport {
			c.JSON(http.StatusOK, gin.H{
				"found":    true,
				"filename": filepath.Base(newestPath),
				"size":     newestSize,
				"mod_time": newestMod.Format(time.RFC3339),
			})
			return
		}

		file, err := os.Open(newestPath)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "open_failed", "failed to open discovered export file", err.Error())
			return
		}
		defer file.Close()

		ctx := c.Request.Context()
		imported, err := ingest.Run(ctx, opts.Store, ingest.Options{
			Source: "chatlog-keeper",
			Reader: file,
		})
		if err != nil {
			writeError(c, http.StatusBadRequest, "import_failed", "could not parse chat export", err.Error())
			return
		}

		ha := &analyze.HighlightsAnalyzer{}
		for _, talker := range imported.Talkers {
			_ = opts.Store.RebuildBehaviorMetrics(ctx, talker)
			_ = ha.Run(ctx, opts.Store, analyze.Options{Talker: talker}, nil)
		}

		c.JSON(http.StatusOK, gin.H{
			"ok":       true,
			"found":    true,
			"filename": filepath.Base(newestPath),
			"talkers":  imported.Talkers,
			"inserted": imported.Inserted,
			"parsed":   imported.Parsed,
		})
	}
}

func isCandidateExport(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	prefix := string(buf[:n])
	return strings.Contains(prefix, "conversation_id") || strings.Contains(prefix, "msg_type") || strings.Contains(prefix, "sender")
}

func syncSidecarHandler(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		talker, _ := resolveTalker(ctx, c.Query("talker"), opts.Store)

		python := "python3"
		if p := os.Getenv("LOVER_PYTHON"); p != "" {
			python = p
		}

		args := []string{"-m", "chatlog_keeper.cli", "wechat", "--lover", "--days=30"}
		if talker != "" {
			args = append(args, "--conversation", talker)
		}

		cmd := exec.CommandContext(ctx, python, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			if path, lookErr := exec.LookPath("chatlog-keeper"); lookErr == nil {
				cmd = exec.CommandContext(ctx, path, "wechat", "--lover", "--days=30")
				if talker != "" {
					cmd.Args = append(cmd.Args, "--conversation", talker)
				}
				out, err = cmd.CombinedOutput()
			}
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, "sidecar_failed",
				"微信聊天记录自动导出失败（请确保电脑已登录微信）", string(out))
			return
		}

		exportPath := filepath.Join(app.ExportsDir(), "auto_sync", "wechat_messages.json")
		file, err := os.Open(exportPath)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "open_failed", "未找到导出的消息文件", err.Error())
			return
		}
		defer file.Close()

		imported, err := ingest.Run(ctx, opts.Store, ingest.Options{
			Source: "chatlog-keeper",
			Reader: file,
		})
		if err != nil {
			writeError(c, http.StatusInternalServerError, "ingest_failed", "导入最新消息失败", err.Error())
			return
		}

		ha := &analyze.HighlightsAnalyzer{}
		for _, t := range imported.Talkers {
			_ = opts.Store.RebuildBehaviorMetrics(ctx, t)
			_ = ha.Run(ctx, opts.Store, analyze.Options{Talker: t}, nil)
		}

		c.JSON(http.StatusOK, gin.H{
			"ok":       true,
			"talkers":  imported.Talkers,
			"inserted": imported.Inserted,
			"parsed":   imported.Parsed,
		})
	}
}
