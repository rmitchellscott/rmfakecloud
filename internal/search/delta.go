package search

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ddvk/rmfakecloud/internal/storage"
	"github.com/ddvk/rmfakecloud/internal/storage/models"
	log "github.com/sirupsen/logrus"
)

const (
	// PageSize is the number of changes to return per delta request
	// This matches the reMarkable cloud's pagination behavior (~100 items)
	PageSize = 100
)

type DeltaTracker struct {
	dataDir string
	mu      sync.RWMutex
	changes []PageChange
}

func NewDeltaTracker(dataDir string) (*DeltaTracker, error) {
	dt := &DeltaTracker{
		dataDir: dataDir,
		changes: []PageChange{},
	}

	if err := dt.load(); err != nil {
		log.Warnf("Failed to load delta tracker: %v", err)
	}

	return dt, nil
}

func (dt *DeltaTracker) deltaFilePath(uid string) string {
	return filepath.Join(dt.dataDir, uid, "search_delta.json")
}

func (dt *DeltaTracker) load() error {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	return nil
}

func (dt *DeltaTracker) save(uid string) error {
	deltaFile := dt.deltaFilePath(uid)
	if err := os.MkdirAll(filepath.Dir(deltaFile), 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(dt.changes, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(deltaFile, data, 0600)
}

func (dt *DeltaTracker) loadUser(uid string) ([]PageChange, error) {
	deltaFile := dt.deltaFilePath(uid)

	data, err := os.ReadFile(deltaFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []PageChange{}, nil
		}
		return nil, err
	}

	var changes []PageChange
	if err := json.Unmarshal(data, &changes); err != nil {
		return nil, err
	}

	return changes, nil
}

func (dt *DeltaTracker) findDocumentForPage(uid, pageID string) (string, error) {
	rootPath := filepath.Join(dt.dataDir, uid, ".sync", "root")
	rootFile, err := os.Open(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // No root yet, that's OK
		}
		return "", err
	}
	defer rootFile.Close()

	// Read root hash
	rootHashBytes, err := io.ReadAll(rootFile)
	if err != nil {
		return "", err
	}
	rootHash := strings.TrimSpace(string(rootHashBytes))

	// Open the root index file
	indexPath := filepath.Join(dt.dataDir, uid, ".sync", rootHash)
	indexFile, err := os.Open(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // Index not yet available
		}
		return "", err
	}
	defer indexFile.Close()

	// Parse root index to get document entries
	docEntries, err := models.ParseIndex(indexFile)
	if err != nil {
		log.Debugf("Failed to parse root index: %v", err)
		return "", nil
	}

	// For each document, check its content index
	for _, docEntry := range docEntries {
		docIndexPath := filepath.Join(dt.dataDir, uid, ".sync", docEntry.Hash)
		docIndexFile, err := os.Open(docIndexPath)
		if err != nil {
			continue // Skip if we can't open this document's index
		}

		fileEntries, err := models.ParseIndex(docIndexFile)
		docIndexFile.Close()
		if err != nil {
			continue
		}

		// Check if any file in this document matches our pageID
		for _, fileEntry := range fileEntries {
			if strings.HasSuffix(fileEntry.EntryName, storage.RmFileExt) {
				filePageID := strings.TrimSuffix(fileEntry.EntryName, storage.RmFileExt)
				if filePageID == pageID {
					return docEntry.EntryName, nil
				}
			}
		}
	}

	return "", nil // Page not found in any document
}

func (dt *DeltaTracker) TrackPageChange(uid, documentID, pageID string, generation int64) error {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	changes, err := dt.loadUser(uid)
	if err != nil {
		return err
	}

	// If documentID is empty, try to find it
	if documentID == "" {
		foundDocID, err := dt.findDocumentForPage(uid, pageID)
		if err != nil {
			log.Warnf("Failed to find document for page %s: %v", pageID, err)
		} else if foundDocID != "" {
			documentID = foundDocID
			log.Debugf("Found documentID %s for pageID %s", documentID, pageID)
		}
	}

	change := PageChange{
		Generation: generation,
		DocumentID: documentID,
		PageID:     pageID,
	}

	found := false
	for i, c := range changes {
		if c.PageID == pageID {
			changes[i].Generation = generation
			changes[i].DocumentID = documentID // Update document ID if we found it
			found = true
			break
		}
	}

	if !found {
		changes = append(changes, change)
	}

	dt.changes = changes
	return dt.save(uid)
}

func (dt *DeltaTracker) getAllCurrentPages(uid string) ([]PageChange, int64, error) {
	rootPath := filepath.Join(dt.dataDir, uid, ".sync", "root")
	rootFile, err := os.Open(rootPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []PageChange{}, 0, nil // No root yet, return empty
		}
		return nil, 0, err
	}
	defer rootFile.Close()

	// Read root hash
	rootHashBytes, err := io.ReadAll(rootFile)
	if err != nil {
		return nil, 0, err
	}
	rootHash := strings.TrimSpace(string(rootHashBytes))

	// Open the root index file
	indexPath := filepath.Join(dt.dataDir, uid, ".sync", rootHash)
	indexFile, err := os.Open(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []PageChange{}, 0, nil
		}
		return nil, 0, err
	}
	defer indexFile.Close()

	// Parse root index to get document entries
	docEntries, err := models.ParseIndex(indexFile)
	if err != nil {
		log.Debugf("Failed to parse root index: %v", err)
		return []PageChange{}, 0, nil
	}

	var allPages []PageChange
	currentGeneration := time.Now().UnixNano() / 1000

	// For each document, get all pages
	for _, docEntry := range docEntries {
		docIndexPath := filepath.Join(dt.dataDir, uid, ".sync", docEntry.Hash)
		docIndexFile, err := os.Open(docIndexPath)
		if err != nil {
			continue
		}

		fileEntries, err := models.ParseIndex(docIndexFile)
		docIndexFile.Close()
		if err != nil {
			continue
		}

		// Add all .rm files as pages
		for _, fileEntry := range fileEntries {
			if strings.HasSuffix(fileEntry.EntryName, storage.RmFileExt) {
				pageID := strings.TrimSuffix(fileEntry.EntryName, storage.RmFileExt)
				allPages = append(allPages, PageChange{
					Generation: currentGeneration,
					DocumentID: docEntry.EntryName,
					PageID:     pageID,
					Deleted:    false,
				})
			}
		}
	}

	return allPages, currentGeneration, nil
}

func (dt *DeltaTracker) cleanupOldChanges(uid string) error {
	// Get all currently existing pages
	currentPages, _, err := dt.getAllCurrentPages(uid)
	if err != nil {
		return err
	}

	// Build a map of current page IDs for fast lookup
	currentPageIDs := make(map[string]bool)
	for _, page := range currentPages {
		currentPageIDs[page.PageID] = true
	}

	// Load tracked changes
	changes, err := dt.loadUser(uid)
	if err != nil {
		return err
	}

	// Filter out pages that no longer exist and mark them as deleted
	updated := false
	for i := range changes {
		if !currentPageIDs[changes[i].PageID] && !changes[i].Deleted {
			// Page no longer exists, mark as deleted
			changes[i].Deleted = true
			changes[i].Generation = time.Now().UnixNano() / 1000
			updated = true
			log.Debugf("Marking page %s as deleted", changes[i].PageID)
		}
	}

	if updated {
		dt.changes = changes
		return dt.save(uid)
	}

	return nil
}

func (dt *DeltaTracker) GetDelta(uid string, since int64) (*DeltaResponse, error) {
	// Periodically cleanup deleted pages (unlock before cleanup to avoid deadlock)
	// Run cleanup every 100 requests or so to avoid overhead
	if since > 0 && since%100 == 0 {
		go func() {
			if err := dt.cleanupOldChanges(uid); err != nil {
				log.Warnf("Failed to cleanup old changes: %v", err)
			}
		}()
	}

	dt.mu.RLock()
	defer dt.mu.RUnlock()

	// Special case: initial sync (since=0)
	// Return all current pages from the root index, with pagination
	if since == 0 {
		allPages, generation, err := dt.getAllCurrentPages(uid)
		if err != nil {
			return nil, err
		}

		if len(allPages) == 0 {
			// No pages yet, return empty
			return &DeltaResponse{
				Version:    1,
				Generation: time.Now().UnixNano() / 1000,
				Latest:     true,
				Changed:    []PageChange{},
			}, nil
		}

		// Sort by generation for consistent pagination
		sort.Slice(allPages, func(i, j int) bool {
			return allPages[i].Generation < allPages[j].Generation
		})

		// Paginate the response
		pageEnd := len(allPages)
		isLatest := true
		responseGeneration := generation

		if len(allPages) > PageSize {
			pageEnd = PageSize
			isLatest = false
			// When paginating, use the generation of the last item in this batch
			responseGeneration = allPages[pageEnd-1].Generation
		}

		log.Infof("Initial delta sync: returning %d/%d pages, latest=%v, generation=%d",
			pageEnd, len(allPages), isLatest, responseGeneration)

		return &DeltaResponse{
			Version:    1,
			Generation: responseGeneration,
			Latest:     isLatest,
			Changed:    allPages[:pageEnd],
		}, nil
	}

	// Subsequent syncs: return only tracked changes, with pagination
	changes, err := dt.loadUser(uid)
	if err != nil {
		return nil, err
	}

	var filtered []PageChange
	var maxGeneration int64 = 0

	for _, change := range changes {
		if change.Generation > since {
			filtered = append(filtered, change)
		}
		if change.Generation > maxGeneration {
			maxGeneration = change.Generation
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Generation < filtered[j].Generation
	})

	if maxGeneration == 0 {
		maxGeneration = time.Now().UnixNano() / 1000
	}

	// Paginate the response
	pageEnd := len(filtered)
	isLatest := true
	responseGeneration := maxGeneration

	if len(filtered) > PageSize {
		pageEnd = PageSize
		isLatest = false
		// When paginating, use the generation of the last item in this batch
		responseGeneration = filtered[pageEnd-1].Generation
	}

	log.Infof("Delta sync: returning %d/%d changes, latest=%v, generation=%d",
		pageEnd, len(filtered), isLatest, responseGeneration)

	return &DeltaResponse{
		Version:    1,
		Generation: responseGeneration,
		Latest:     isLatest,
		Changed:    filtered[:pageEnd],
	}, nil
}
