package search

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ddvk/rmfakecloud/internal/hwr"
	"github.com/ddvk/rmfakecloud/internal/storage"
	"github.com/ddvk/rmfakecloud/internal/storage/models"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	deltaTracker *DeltaTracker
	indexManager *IndexManager
	dataDir      string
	indexQueue   chan indexJob
	jobQueue     *JobQueue
	userStorer   storage.UserStorer
}

type indexJob struct {
	uid        string
	docID      string
	pageID     string
	generation int64
}

func NewHandler(deltaTracker *DeltaTracker, indexManager *IndexManager, dataDir string, userStorer storage.UserStorer) *Handler {
	h := &Handler{
		deltaTracker: deltaTracker,
		indexManager: indexManager,
		dataDir:      dataDir,
		indexQueue:   make(chan indexJob, 100),
		jobQueue:     NewJobQueue(dataDir),
		userStorer:   userStorer,
	}

	for i := 0; i < 5; i++ {
		go h.indexWorker()
	}

	go h.jobRecoveryWorker()

	go h.loadPendingJobsOnStartup()

	return h
}

func (h *Handler) indexWorker() {
	for job := range h.indexQueue {
		rmFilePath, err := h.getRmFilePath(job.uid, job.docID, job.pageID)
		if err != nil {
			log.Warnf("Failed to get rm file path for %s/%s: %v", job.docID, job.pageID, err)
			h.jobQueue.UpdateJobError(job.uid, job.docID, job.pageID, "other")
			continue
		}

		index, err := h.indexManager.GetOrBuildIndex(job.uid, job.docID, job.pageID, rmFilePath, job.generation)
		if err != nil {
			if errors.Is(err, hwr.ErrQuotaExceeded) {
				log.Warnf("MyScript quota exceeded for %s/%s, will retry daily", job.docID, job.pageID)
				h.jobQueue.UpdateJobError(job.uid, job.docID, job.pageID, "quota")
			} else {
				log.Warnf("Failed to build index for %s/%s: %v", job.docID, job.pageID, err)
				h.jobQueue.UpdateJobError(job.uid, job.docID, job.pageID, "other")
			}
			continue
		}

		if len(index.Handwritten.MainStrokes.Strokes) > 0 {
			log.Infof("Indexed handwriting page %s/%s (%d words)", job.docID, job.pageID, len(index.Handwritten.MainStrokes.Strokes))
		}

		if err := h.jobQueue.DeleteJob(job.uid, job.docID, job.pageID); err != nil {
			log.Warnf("Failed to delete pending job for %s/%s: %v", job.docID, job.pageID, err)
		}
	}
}

func (h *Handler) GetSettings(c *gin.Context) {
	uid := c.GetString("UserID")
	searchEnabled := false

	if uid != "" {
		user, err := h.userStorer.GetUser(uid)
		if err == nil && user != nil {
			searchEnabled = user.Search
		}
	}

	response := SettingsResponse{
		Language:      "en_US",
		SearchEnabled: searchEnabled,
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

	since := int64(0)
	if ifNoneMatch := c.GetHeader("if-none-match"); ifNoneMatch != "" {
		var err error
		since, err = strconv.ParseInt(ifNoneMatch, 10, 64)
		if err != nil {
			log.Warnf("Invalid if-none-match value: %s", ifNoneMatch)
			since = 0
		}
	}

	log.Debugf("Delta request from user %s, since=%d", uid, since)

	delta, err := h.deltaTracker.GetDelta(uid, since)
	if err != nil {
		log.Errorf("Failed to get delta: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get delta"})
		return
	}

	if len(delta.Changed) == 0 {
		log.Debugf("Delta response: 304 Not Modified (no changes since %d)", since)
		c.Status(http.StatusNotModified)
		return
	}

	log.Debugf("Delta response: 200 OK - %+v", delta)

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

	log.Debugf("Reading .rm file from: %s", rmFilePath)
	if _, err := os.Stat(rmFilePath); err != nil {
		log.Warnf("Failed to stat file: %v", err)
	}

	fileInfo, err := os.Stat(rmFilePath)
	if err != nil {
		log.Errorf("Failed to stat .rm file: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to stat file"})
		return
	}
	generation := fileInfo.ModTime().UnixNano() / 1000

	log.Debugf("Search request for %s/%s (file mtime generation=%d)", docID, pageID, generation)

	index, err := h.indexManager.GetOrBuildIndex(uid, docID, pageID, rmFilePath, generation)
	if err != nil {
		log.Errorf("Failed to build search index: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build search index"})
		return
	}

	c.JSON(http.StatusOK, index)
}

func (h *Handler) getRmFilePath(uid, docID, pageID string) (string, error) {
	rootPath := filepath.Join(h.dataDir, "users", uid, "sync", "root")
	rootData, err := os.ReadFile(rootPath)
	if err != nil {
		return "", fmt.Errorf("failed to read root: %w", err)
	}
	rootHash := strings.TrimSpace(string(rootData))

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

	targetName := fmt.Sprintf("%s/%s.rm", docID, pageID)
	for _, entry := range pageEntries {
		if entry.EntryName == targetName {
			return filepath.Join(h.dataDir, "users", uid, "sync", entry.Hash), nil
		}
	}

	return "", fmt.Errorf("page %s not found in document %s", pageID, docID)
}

func (h *Handler) TrackPageModification(uid, docID, pageID string, generation int64) error {
	if err := h.jobQueue.SaveJob(uid, docID, pageID, generation); err != nil {
		log.Errorf("Failed to save pending job for %s/%s: %v", docID, pageID, err)
		return err
	}

	select {
	case h.indexQueue <- indexJob{uid, docID, pageID, generation}:
	default:
		log.Warnf("Index queue full, job saved to disk for later processing: %s/%s", docID, pageID)
	}

	return nil
}

func (h *Handler) jobRecoveryWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		allJobs, err := h.jobQueue.GetAllPendingJobs()
		if err != nil {
			log.Errorf("Failed to get pending jobs: %v", err)
			continue
		}

		totalJobs := 0
		for _, jobs := range allJobs {
			totalJobs += len(jobs)
		}

		if totalJobs == 0 {
			continue
		}

		for _, jobs := range allJobs {
			for _, job := range jobs {
				var backoffDuration time.Duration
				var shouldRetry bool

				if job.ErrorType == "quota" {
					// Quota errors: retry once per day indefinitely
					backoffDuration = 24 * time.Hour
					shouldRetry = true
				} else {
					// Other errors: exponential backoff capped at 1 hour, max 10 retries
					if job.RetryCount >= 10 {
						log.Warnf("Job %s/%s has failed %d times (non-quota error), giving up", job.DocID, job.PageID, job.RetryCount)
						shouldRetry = false
					} else {
						// Exponential backoff: 30s * 2^retryCount, capped at 1 hour
						backoffSeconds := 30 * (1 << uint(job.RetryCount))
						if backoffSeconds > 3600 {
							backoffSeconds = 3600
						}
						backoffDuration = time.Duration(backoffSeconds) * time.Second
						shouldRetry = true
					}
				}

				if !shouldRetry {
					continue
				}

				timeSinceLastAttempt := time.Since(job.LastAttempt)
				if timeSinceLastAttempt < backoffDuration {
					continue
				}

				select {
				case h.indexQueue <- indexJob{job.UID, job.DocID, job.PageID, job.Generation}:
				default:
					return
				}
			}
		}
	}
}

func (h *Handler) loadPendingJobsOnStartup() {
	time.Sleep(5 * time.Second)

	allJobs, err := h.jobQueue.GetAllPendingJobs()
	if err != nil {
		log.Errorf("Startup: failed to load pending jobs: %v", err)
		return
	}

	totalJobs := 0
	for _, jobs := range allJobs {
		totalJobs += len(jobs)
	}

	if totalJobs == 0 {
		log.Info("Startup: no pending jobs to recover")
		return
	}

	log.Infof("Startup: loading %d pending jobs from disk", totalJobs)

	for _, jobs := range allJobs {
		for _, job := range jobs {
			if job.ErrorType != "quota" && job.RetryCount >= 10 {
				log.Warnf("Startup: job %s/%s has failed %d times (non-quota), giving up", job.DocID, job.PageID, job.RetryCount)
				continue
			}

			select {
			case h.indexQueue <- indexJob{job.UID, job.DocID, job.PageID, job.Generation}:
			default:
				return
			}
		}
	}

	log.Infof("Startup: finished loading pending jobs")
}

func (h *Handler) HandleError(c *gin.Context) {
	c.Status(http.StatusAccepted)
}

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	searchV1 := router.Group("/search/v1")
	{
		searchV1.GET("/settings", handler.GetSettings)
		searchV1.GET("/delta", handler.GetDelta)
		searchV1.GET("/:docId/:pageId", handler.GetSearchIndex)
	}
}
