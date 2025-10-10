package search

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ddvk/rmfakecloud/internal/storage/models"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	deltaTracker *DeltaTracker
	indexManager *IndexManager
	dataDir      string
	indexQueue   chan indexJob
}

type indexJob struct {
	uid        string
	docID      string
	pageID     string
	generation int64
}

func NewHandler(deltaTracker *DeltaTracker, indexManager *IndexManager, dataDir string) *Handler {
	h := &Handler{
		deltaTracker: deltaTracker,
		indexManager: indexManager,
		dataDir:      dataDir,
		indexQueue:   make(chan indexJob, 100),
	}

	// Start 5 worker goroutines
	for i := 0; i < 5; i++ {
		go h.indexWorker()
	}

	return h
}

func (h *Handler) indexWorker() {
	for job := range h.indexQueue {
		rmFilePath, err := h.getRmFilePath(job.uid, job.docID, job.pageID)
		if err != nil {
			log.Warnf("Failed to get rm file path for %s/%s: %v", job.docID, job.pageID, err)
			continue
		}

		index, err := h.indexManager.GetOrBuildIndex(job.uid, job.docID, job.pageID, rmFilePath, job.generation)
		if err != nil {
			log.Warnf("Failed to build index for %s/%s: %v", job.docID, job.pageID, err)
			continue
		}

		if len(index.Handwritten.MainStrokes.Strokes) > 0 {
			log.Infof("Indexed handwriting page %s/%s (%d words)", job.docID, job.pageID, len(index.Handwritten.MainStrokes.Strokes))
		} else {
			log.Debugf("Indexed typed text page %s/%s (no handwriting)", job.docID, job.pageID)
		}
	}
}

func (h *Handler) GetSettings(c *gin.Context) {
	response := SettingsResponse{
		Language:      "en_US",
		SearchEnabled: true,
	}

	c.JSON(http.StatusOK, response)
}

func (h *Handler) GetDelta(c *gin.Context) {
	uid := c.GetString("UserID")
	if uid == "" {
		log.Warnf("Delta request with no UserID")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Parse if-none-match header as "since" checkpoint
	since := int64(0)
	if ifNoneMatch := c.GetHeader("if-none-match"); ifNoneMatch != "" {
		var err error
		since, err = strconv.ParseInt(ifNoneMatch, 10, 64)
		if err != nil {
			log.Warnf("Invalid if-none-match value: %s", ifNoneMatch)
			since = 0
		}
	}

	log.Infof("Delta request from user %s, since=%d", uid, since)

	delta, err := h.deltaTracker.GetDelta(uid, since)
	if err != nil {
		log.Errorf("Failed to get delta: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get delta"})
		return
	}

	if len(delta.Changed) == 0 {
		log.Infof("Delta response: 304 Not Modified (no changes since %d)", since)
		c.Status(http.StatusNotModified)
		return
	}

	log.Infof("Delta response: 200 OK - %+v", delta)

	// Set ETag header with the generation we're returning
	c.Header("ETag", fmt.Sprintf("%d", delta.Generation))
	c.JSON(http.StatusOK, delta)
}

func (h *Handler) GetSearchIndex(c *gin.Context) {
	uid := c.GetString("UserID")
	if uid == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	docID := c.Param("docId")
	pageID := c.Param("pageId")

	if docID == "" || pageID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing docId or pageId"})
		return
	}

	rmFilePath, err := h.getRmFilePath(uid, docID, pageID)
	if err != nil {
		log.Errorf("Failed to find .rm file: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}

	log.Infof("Reading .rm file from: %s", rmFilePath)
	if fileInfo, err := os.Stat(rmFilePath); err == nil {
		log.Debugf("File size: %d bytes", fileInfo.Size())
	} else {
		log.Warnf("Failed to stat file: %v", err)
	}

	// Use timestamp-based generation (microseconds since epoch)
	// This matches the device's generation format and enables delta sync
	generation := time.Now().UnixNano() / 1000

	log.Infof("Search request for %s/%s (generation=%d)", docID, pageID, generation)

	index, err := h.indexManager.GetOrBuildIndex(uid, docID, pageID, rmFilePath, generation)
	if err != nil {
		log.Errorf("Failed to build search index: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build search index"})
		return
	}

	// Set version and generation in response
	index.Version = 1
	index.Generation = generation

	c.JSON(http.StatusOK, index)
}

func (h *Handler) getRmFilePath(uid, docID, pageID string) (string, error) {
	// Read root to get root hash
	rootPath := filepath.Join(h.dataDir, "users", uid, "sync", "root")
	rootData, err := os.ReadFile(rootPath)
	if err != nil {
		return "", fmt.Errorf("failed to read root: %w", err)
	}
	rootHash := strings.TrimSpace(string(rootData))

	// Parse root index to find document
	rootIndexPath := filepath.Join(h.dataDir, "users", uid, "sync", rootHash)
	rootIndexFile, err := os.Open(rootIndexPath)
	if err != nil {
		return "", fmt.Errorf("failed to open root index: %w", err)
	}
	defer rootIndexFile.Close()

	docEntries, err := models.ParseIndex(rootIndexFile)
	if err != nil {
		return "", fmt.Errorf("failed to parse root index: %w", err)
	}

	// Find the document entry
	var docHash string
	for _, entry := range docEntries {
		if entry.EntryName == docID {
			docHash = entry.Hash
			break
		}
	}
	if docHash == "" {
		return "", fmt.Errorf("document %s not found", docID)
	}

	// Parse document index to find page
	docIndexPath := filepath.Join(h.dataDir, "users", uid, "sync", docHash)
	docIndexFile, err := os.Open(docIndexPath)
	if err != nil {
		return "", fmt.Errorf("failed to open document index: %w", err)
	}
	defer docIndexFile.Close()

	pageEntries, err := models.ParseIndex(docIndexFile)
	if err != nil {
		return "", fmt.Errorf("failed to parse document index: %w", err)
	}

	// Find the page entry (looking for {docID}/{pageID}.rm)
	targetName := fmt.Sprintf("%s/%s.rm", docID, pageID)
	for _, entry := range pageEntries {
		if entry.EntryName == targetName {
			return filepath.Join(h.dataDir, "users", uid, "sync", entry.Hash), nil
		}
	}

	return "", fmt.Errorf("page %s not found in document %s", pageID, docID)
}

func (h *Handler) TrackPageModification(uid, docID, pageID string, generation int64) error {
	// Submit job to worker pool (non-blocking)
	select {
	case h.indexQueue <- indexJob{uid, docID, pageID, generation}:
		log.Debugf("Queued indexing job for %s/%s", docID, pageID)
	default:
		log.Warnf("Index queue full, dropping job for %s/%s", docID, pageID)
	}

	return nil
}

func (h *Handler) HandleError(c *gin.Context) {
	// Device reports search errors here - just accept them
	log.Debug("Received search error report from device")
	c.Status(http.StatusAccepted)
}

func (h *Handler) logRequestToFile(method, url string, headers http.Header, body []byte) {
	logPath := filepath.Join(h.dataDir, "search_requests.log")

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Warnf("Failed to open search request log: %v", err)
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000")

	f.WriteString(fmt.Sprintf("\n=== %s %s %s ===\n", timestamp, method, url))
	f.WriteString("Headers:\n")
	for key, values := range headers {
		for _, value := range values {
			f.WriteString(fmt.Sprintf("  %s: %s\n", key, value))
		}
	}

	if len(body) > 0 {
		f.WriteString(fmt.Sprintf("Body: %s\n", string(body)))
	} else {
		f.WriteString("Body: (empty)\n")
	}
	f.WriteString("\n")
}

func (h *Handler) requestLoggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var bodyBytes []byte
		if c.Request.Body != nil {
			bodyBytes, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		h.logRequestToFile(c.Request.Method, c.Request.URL.String(), c.Request.Header, bodyBytes)

		c.Next()
	}
}

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	searchV1 := router.Group("/search/v1")
	searchV1.Use(handler.requestLoggingMiddleware())
	{
		searchV1.GET("/settings", handler.GetSettings)
		searchV1.GET("/delta", handler.GetDelta)
		searchV1.GET("/:docId/:pageId", handler.GetSearchIndex)
	}
}
