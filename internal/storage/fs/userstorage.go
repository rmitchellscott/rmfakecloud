package fs

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ddvk/rmfakecloud/internal/config"
	"github.com/ddvk/rmfakecloud/internal/model"
	log "github.com/sirupsen/logrus"
)

const (
	userDir     = "users"
	profileName = ".userprofile"
)

// NewStorage new file system storage
func NewStorage(cfg *config.Config) *FileSystemStorage {
	fs := &FileSystemStorage{
		Cfg: cfg,
	}

	usersPath := fs.getUserPath("")
	err := os.MkdirAll(usersPath, 0700)
	if err != nil {
		log.Fatal("cannot create the user path " + usersPath)
	}

	return fs
}

// GetUser retrieves a user from the storage
func (fs *FileSystemStorage) GetUser(uid string) (user *model.User, err error) {
	if uid == "" {
		err = errors.New("empty user")
		return
	}
	profilePath := fs.getPathFromUser(uid, profileName)
	_, err = os.Stat(profilePath)
	if err != nil {
		return
	}

	var f *os.File
	f, err = os.Open(profilePath)
	if err != nil {
		return
	}
	defer f.Close()

	var content []byte
	content, err = io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("cannot read profile: %s, %w", profilePath, err)
	}

	user, err = model.DeserializeUser(content)
	if err != nil {
		return nil, fmt.Errorf("cannot parse profile: %s, %w", profilePath, err)
	}

	return
}

// GetUsers gets all users
func (fs *FileSystemStorage) GetUsers() (users []*model.User, err error) {
	usersDir := fs.getUserPath("")

	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if user, err := fs.GetUser(entry.Name()); err == nil {
				users = append(users, user)
			}
		}
	}
	return
}

// RegisterUser blah
func (fs *FileSystemStorage) RegisterUser(u *model.User) (err error) {
	if u.ID == "" {
		err = errors.New("empty id")
		return
	}
	userBlobPath := fs.getUserBlobPath(u.ID)

	// Create the user's directory
	err = os.MkdirAll(userBlobPath, 0700)
	if err != nil {
		return
	}

	profilePath := fs.getPathFromUser(u.ID, profileName)
	// Create the profile file
	js, err := u.Serialize()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(profilePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		log.Warn("cant open: ", profilePath)
		return err
	}
	defer f.Close()
	_, err = f.Write(js)
	if err != nil {
		log.Warn("could not write ", profilePath)
		return err
	}

	return
}

// UpdateUser updates the user
func (fs *FileSystemStorage) UpdateUser(u *model.User) (err error) {
	if u.ID == "" {
		err = errors.New("empty id")
		return
	}

	userSyncPath := fs.getUserBlobPath(u.ID)
	err = os.MkdirAll(userSyncPath, 0700)
	if err != nil {
		return
	}

	profilePath := fs.getPathFromUser(u.ID, profileName)
	// Overwrite the profile
	js, err := u.Serialize()
	if err != nil {
		return
	}
	err = os.WriteFile(profilePath, js, 0600)

	return
}

// RemoveUser remove the user and their data
func (fs *FileSystemStorage) RemoveUser(uid string) (err error) {
	if uid == "" {
		err = errors.New("empty id")
		return
	}

	userSyncPath := fs.getUserPath(uid)
	err = os.RemoveAll(userSyncPath)
	if err != nil {
		return
	}

	return
}

// MigrateUserProfiles ensures all user profiles have the search field explicitly set
func (fs *FileSystemStorage) MigrateUserProfiles() error {
	users, err := fs.GetUsers()
	if err != nil {
		return fmt.Errorf("failed to get users for migration: %w", err)
	}

	migratedCount := 0
	for _, user := range users {
		profilePath := fs.getPathFromUser(user.ID, profileName)

		// Read raw profile content to check if search field exists
		content, err := os.ReadFile(profilePath)
		if err != nil {
			log.Warnf("Failed to read profile for migration check: %s: %v", user.ID, err)
			continue
		}

		// Check if the search field is present in the YAML
		// This is a simple string check - if "search:" appears, we assume it's already migrated
		if !containsSearchField(content) {
			// Field is missing, update the profile to include it
			user.Search = false
			if err := fs.UpdateUser(user); err != nil {
				log.Warnf("Failed to migrate user profile %s: %v", user.ID, err)
				continue
			}
			migratedCount++
			log.Infof("Migrated user profile %s: added search: false", user.ID)
		}
	}

	if migratedCount > 0 {
		log.Infof("User profile migration complete: updated %d profiles", migratedCount)
	}

	return nil
}

// containsSearchField checks if the YAML content contains the search field
func containsSearchField(content []byte) bool {
	// Simple check: look for "search:" in the YAML content
	// This works because YAML field names are always followed by a colon
	for i := 0; i < len(content)-7; i++ {
		if content[i] == 's' &&
		   content[i+1] == 'e' &&
		   content[i+2] == 'a' &&
		   content[i+3] == 'r' &&
		   content[i+4] == 'c' &&
		   content[i+5] == 'h' &&
		   content[i+6] == ':' {
			return true
		}
	}
	return false
}
