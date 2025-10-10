package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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

func (dt *DeltaTracker) TrackPageChange(uid, documentID, pageID string, generation int64) error {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	changes, err := dt.loadUser(uid)
	if err != nil {
		return err
	}

	change := PageChange{
		Generation: generation,
		DocumentID: documentID,
		PageID:     pageID,
	}

	found := false
	for i, c := range changes {
		if c.DocumentID == documentID && c.PageID == pageID {
			changes[i].Generation = generation
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

func (dt *DeltaTracker) GetDelta(uid string, since int64) (*DeltaResponse, error) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

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

	return &DeltaResponse{
		Version:    1,
		Generation: maxGeneration,
		Latest:     true,
		Changed:    filtered,
	}, nil
}
