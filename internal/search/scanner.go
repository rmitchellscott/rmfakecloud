package search

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ddvk/rmfakecloud/internal/storage/models"
	log "github.com/sirupsen/logrus"
)

// ScanUserDocuments walks the user's sync tree and queues all .rm files for indexing.
// It respects existing cache to avoid unnecessary re-indexing.
// Returns (filesQueued, filesSkipped, error)
func ScanUserDocuments(uid string, dataDir string, handler *Handler) (int, int, error) {
	queued := 0
	skipped := 0

	// Read root hash
	rootPath := filepath.Join(dataDir, "users", uid, "sync", "root")
	rootData, err := os.ReadFile(rootPath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read root: %w", err)
	}
	rootHash := strings.TrimSpace(string(rootData))

	// Parse root index
	rootIndexPath := filepath.Join(dataDir, "users", uid, "sync", rootHash)
	rootIndexFile, err := os.Open(rootIndexPath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open root index: %w", err)
	}
	defer rootIndexFile.Close()

	docEntries, err := models.ParseIndex(rootIndexFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse root index: %w", err)
	}

	// For each document
	for _, docEntry := range docEntries {
		docID := docEntry.EntryName
		docHash := docEntry.Hash

		// Parse document index
		docIndexPath := filepath.Join(dataDir, "users", uid, "sync", docHash)
		docIndexFile, err := os.Open(docIndexPath)
		if err != nil {
			log.Warnf("Failed to open document index for %s: %v", docID, err)
			continue
		}

		pageEntries, err := models.ParseIndex(docIndexFile)
		docIndexFile.Close()
		if err != nil {
			log.Warnf("Failed to parse document index for %s: %v", docID, err)
			continue
		}

		// For each .rm file in this document
		for _, pageEntry := range pageEntries {
			// Check if this is a .rm file
			if !strings.HasSuffix(pageEntry.EntryName, ".rm") {
				continue
			}

			// Extract docID and pageID from entry name
			// Format: {docID}/{pageID}.rm
			parts := strings.Split(pageEntry.EntryName, "/")
			if len(parts) != 2 {
				log.Warnf("Unexpected .rm file name format: %s", pageEntry.EntryName)
				continue
			}
			entryDocID := parts[0]
			pageID := strings.TrimSuffix(parts[1], ".rm")

			// Resolve hash to filesystem path
			rmFilePath := filepath.Join(dataDir, "users", uid, "sync", pageEntry.Hash)

			// Get file generation (mtime)
			fileInfo, err := os.Stat(rmFilePath)
			if err != nil {
				log.Warnf("Failed to stat .rm file %s/%s: %v", entryDocID, pageID, err)
				continue
			}
			generation := fileInfo.ModTime().UnixNano() / 1000

			// Check if we need to index this page
			if shouldIndexPage(handler.indexManager, uid, entryDocID, pageID, generation) {
				// Queue for indexing
				if err := handler.TrackPageModification(uid, entryDocID, pageID, generation); err != nil {
					log.Warnf("Failed to queue page %s/%s: %v", entryDocID, pageID, err)
					continue
				}
				queued++
			} else {
				skipped++
			}
		}
	}

	return queued, skipped, nil
}

// shouldIndexPage checks if a page needs to be indexed by comparing the cache generation
func shouldIndexPage(im *IndexManager, uid, docID, pageID string, generation int64) bool {
	cachePath := im.cacheFilePath(uid, docID, pageID)
	cached, err := im.loadCachedIndex(cachePath)
	if err != nil {
		// No cache or error reading cache = need to index
		return true
	}
	// Cache exists - check if generation matches
	return cached.Generation != generation
}
