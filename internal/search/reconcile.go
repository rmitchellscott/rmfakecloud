package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type scanState struct {
	RootHash   string    `json:"rootHash"`
	Complete   bool      `json:"complete"`
	LastRun    time.Time `json:"lastRun"`
	LastQueued int       `json:"lastQueued"`
}

func (h *Handler) scanStatePath(uid string) string {
	return filepath.Join(h.dataDir, "users", uid, "search", "scan.json")
}

func (h *Handler) loadScanState(uid string) *scanState {
	data, err := os.ReadFile(h.scanStatePath(uid))
	if err != nil {
		return &scanState{}
	}

	var state scanState
	if err := json.Unmarshal(data, &state); err != nil {
		return &scanState{}
	}
	return &state
}

func (h *Handler) saveScanState(uid string, state *scanState) {
	path := h.scanStatePath(uid)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		log.Warnf("Cannot create scan state dir for %s: %v", uid, err)
		return
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Warnf("Cannot marshal scan state for %s: %v", uid, err)
		return
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Warnf("Cannot write scan state for %s: %v", uid, err)
	}
}

func (h *Handler) currentRootHash(uid string) (string, error) {
	data, err := os.ReadFile(filepath.Join(h.dataDir, "users", uid, "sync", "root"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// StartReconciler sweeps for pages the upload path never indexed.
func (h *Handler) StartReconciler(interval time.Duration) {
	if interval <= 0 {
		log.Info("Search reconciler disabled")
		return
	}

	go func() {
		time.Sleep(30 * time.Second)
		h.reconcileAllUsers()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			h.reconcileAllUsers()
		}
	}()

	log.Infof("Search reconciler started, interval %s", interval)
}

func (h *Handler) reconcileAllUsers() {
	if h.quotaBlocked() {
		log.Info("Reconcile skipped, MyScript quota is exhausted")
		return
	}

	users, err := h.userStorer.GetUsers()
	if err != nil {
		log.Errorf("Reconcile cannot list users: %v", err)
		return
	}

	for _, user := range users {
		if user == nil || !user.Search {
			continue
		}
		h.reconcileUser(user.ID)
	}
}

func (h *Handler) reconcileUser(uid string) {
	rootHash, err := h.currentRootHash(uid)
	if err != nil {
		log.Debugf("Reconcile skipping %s, no sync root: %v", uid, err)
		return
	}

	state := h.loadScanState(uid)
	if state.Complete && state.RootHash == rootHash {
		return
	}

	queued, skipped, err := ScanUserDocuments(uid, h.dataDir, h)
	if err != nil {
		log.Warnf("Reconcile failed for %s: %v", uid, err)
		h.saveScanState(uid, &scanState{RootHash: rootHash, Complete: false, LastRun: time.Now()})
		return
	}

	if queued > 0 {
		log.Infof("Reconcile queued %d unindexed pages for %s (%d already indexed)", queued, uid, skipped)
	}

	h.saveScanState(uid, &scanState{
		RootHash:   rootHash,
		Complete:   true,
		LastRun:    time.Now(),
		LastQueued: queued,
	})
}
