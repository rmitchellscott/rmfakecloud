package search

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	deltaTracker *DeltaTracker
	indexManager *IndexManager
	dataDir      string
}

func NewHandler(deltaTracker *DeltaTracker, indexManager *IndexManager, dataDir string) *Handler {
	return &Handler{
		deltaTracker: deltaTracker,
		indexManager: indexManager,
		dataDir:      dataDir,
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	sinceStr := c.Query("since")
	since := int64(0)
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since parameter"})
			return
		}
	}

	delta, err := h.deltaTracker.GetDelta(uid, since)
	if err != nil {
		log.Errorf("Failed to get delta: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get delta"})
		return
	}

	if len(delta.Changed) == 0 {
		c.Status(http.StatusNotModified)
		return
	}

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

	rmFilePath := h.getRmFilePath(uid, pageID)

	generation := c.GetInt64("generation")
	if generation == 0 {
		generation = c.GetInt64("currentGeneration")
	}

	log.Infof("Search request for %s/%s (generation=%d)", docID, pageID, generation)

	index, err := h.indexManager.GetOrBuildIndex(uid, docID, pageID, rmFilePath, generation)
	if err != nil {
		log.Errorf("Failed to build search index: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build search index"})
		return
	}

	c.JSON(http.StatusOK, index)
}

func (h *Handler) getRmFilePath(uid, pageID string) string {
	return filepath.Join(h.dataDir, uid, ".sync", fmt.Sprintf("%s.rm", pageID))
}

func (h *Handler) TrackPageModification(uid, docID, pageID string, generation int64) error {
	return h.deltaTracker.TrackPageChange(uid, docID, pageID, generation)
}

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	searchV1 := router.Group("/search/v1")
	{
		searchV1.GET("/settings", handler.GetSettings)
		searchV1.GET("/delta", handler.GetDelta)
		searchV1.GET("/:docId/:pageId", handler.GetSearchIndex)
	}
}
