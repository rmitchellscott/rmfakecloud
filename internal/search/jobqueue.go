package search

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type PendingJob struct {
	UID         string    `json:"uid"`
	DocID       string    `json:"docId"`
	PageID      string    `json:"pageId"`
	BlobHash    string    `json:"blobHash,omitempty"`
	Generation  int64     `json:"generation"`
	Timestamp   time.Time `json:"timestamp"`
	RetryCount  int       `json:"retryCount"`
	LastAttempt time.Time `json:"lastAttempt"`
	ErrorType   string    `json:"errorType"` // "quota" or "other"
}

type JobQueue struct {
	dataDir string
	mu      sync.Mutex
}

func NewJobQueue(dataDir string) *JobQueue {
	return &JobQueue{
		dataDir: dataDir,
	}
}

func (jq *JobQueue) pendingDir(uid string) string {
	return filepath.Join(jq.dataDir, "users", uid, "search", "pending")
}

func (jq *JobQueue) jobFilePath(uid, docID, pageID string) string {
	filename := fmt.Sprintf("%s-%s.json", docID, pageID)
	return filepath.Join(jq.pendingDir(uid), filename)
}

func (jq *JobQueue) SaveJob(uid, docID, pageID, blobHash string, generation int64) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	now := time.Now()
	job := PendingJob{
		UID:         uid,
		DocID:       docID,
		PageID:      pageID,
		BlobHash:    blobHash,
		Generation:  generation,
		Timestamp:   now,
		RetryCount:  0,
		LastAttempt: now,
		ErrorType:   "",
	}

	pendingDir := jq.pendingDir(uid)
	if err := os.MkdirAll(pendingDir, 0700); err != nil {
		return fmt.Errorf("failed to create pending dir: %w", err)
	}

	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	jobPath := jq.jobFilePath(uid, docID, pageID)
	if err := os.WriteFile(jobPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write job file: %w", err)
	}

	return nil
}

func (jq *JobQueue) DeleteJob(uid, docID, pageID string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	jobPath := jq.jobFilePath(uid, docID, pageID)
	if err := os.Remove(jobPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete job file: %w", err)
	}

	return nil
}

func (jq *JobQueue) IncrementRetryCount(uid, docID, pageID string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	jobPath := jq.jobFilePath(uid, docID, pageID)
	data, err := os.ReadFile(jobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read job file: %w", err)
	}

	var job PendingJob
	if err := json.Unmarshal(data, &job); err != nil {
		return fmt.Errorf("failed to parse job file: %w", err)
	}

	job.RetryCount++
	job.LastAttempt = time.Now()

	data, err = json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	if err := os.WriteFile(jobPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write job file: %w", err)
	}

	return nil
}

func (jq *JobQueue) UpdateJobError(uid, docID, pageID string, errorType string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	jobPath := jq.jobFilePath(uid, docID, pageID)
	data, err := os.ReadFile(jobPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read job file: %w", err)
	}

	var job PendingJob
	if err := json.Unmarshal(data, &job); err != nil {
		return fmt.Errorf("failed to parse job file: %w", err)
	}

	job.RetryCount++
	job.LastAttempt = time.Now()
	job.ErrorType = errorType

	data, err = json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	if err := os.WriteFile(jobPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write job file: %w", err)
	}

	return nil
}

func (jq *JobQueue) GetPendingJobs(uid string) ([]PendingJob, error) {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	pendingDir := jq.pendingDir(uid)
	if _, err := os.Stat(pendingDir); os.IsNotExist(err) {
		return []PendingJob{}, nil
	}

	var jobs []PendingJob

	err := filepath.Walk(pendingDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			log.Warnf("Failed to read job file %s: %v", path, err)
			return nil
		}

		var job PendingJob
		if err := json.Unmarshal(data, &job); err != nil {
			log.Warnf("Failed to parse job file %s: %v", path, err)
			return nil
		}

		jobs = append(jobs, job)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk pending directory: %w", err)
	}

	return jobs, nil
}

func (jq *JobQueue) GetAllPendingJobs() (map[string][]PendingJob, error) {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	usersDir := filepath.Join(jq.dataDir, "users")
	if _, err := os.Stat(usersDir); os.IsNotExist(err) {
		return make(map[string][]PendingJob), nil
	}

	result := make(map[string][]PendingJob)

	userDirs, err := os.ReadDir(usersDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read users directory: %w", err)
	}

	for _, userDir := range userDirs {
		if !userDir.IsDir() {
			continue
		}

		uid := userDir.Name()
		pendingDir := jq.pendingDir(uid)

		if _, err := os.Stat(pendingDir); os.IsNotExist(err) {
			continue
		}

		err := filepath.Walk(pendingDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if info.IsDir() || !strings.HasSuffix(path, ".json") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				log.Warnf("Failed to read job file %s: %v", path, err)
				return nil
			}

			var job PendingJob
			if err := json.Unmarshal(data, &job); err != nil {
				log.Warnf("Failed to parse job file %s: %v", path, err)
				return nil
			}

			result[uid] = append(result[uid], job)
			return nil
		})

		if err != nil {
			log.Warnf("Failed to scan pending jobs for user %s: %v", uid, err)
		}
	}

	return result, nil
}
