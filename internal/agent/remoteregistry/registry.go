package remoteregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Mount struct {
	Host       string    `json:"host"`
	RemotePath string    `json:"remote_path"`
	LocalPath  string    `json:"local_path"`
	MountedAt  time.Time `json:"mounted_at"`
	Status     string    `json:"status"` // "active", "stale", "disconnected"
}

type Session struct {
	Host        string    `json:"host"`
	SessionName string    `json:"session_name"`
	LastSeen    time.Time `json:"last_seen"`
}

type RegistryData struct {
	Mounts   []Mount   `json:"mounts"`
	Sessions []Session `json:"sessions"`
}

type Registry struct {
	mu       sync.Mutex
	filePath string
	data     RegistryData
}

func NewRegistry(dataDir string) (*Registry, error) {
	filePath := filepath.Join(dataDir, "remote_workspace.json")
	r := &Registry{
		filePath: filePath,
	}
	if err := r.load(); err != nil {
		if os.IsNotExist(err) {
			r.data = RegistryData{
				Mounts:   []Mount{},
				Sessions: []Session{},
			}
			return r, nil
		}
		return nil, err
	}
	return r, nil
}

func (r *Registry) load() error {
	b, err := os.ReadFile(r.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &r.data)
}

func (r *Registry) save() error {
	b, err := json.MarshalIndent(r.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.filePath), 0700); err != nil {
		return err
	}
	return os.WriteFile(r.filePath, b, 0600)
}

func (r *Registry) AddMount(mount Mount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Check for duplicates
	for i, m := range r.data.Mounts {
		if m.Host == mount.Host && m.RemotePath == mount.RemotePath {
			r.data.Mounts[i] = mount
			return r.save()
		}
	}
	r.data.Mounts = append(r.data.Mounts, mount)
	return r.save()
}

func (r *Registry) RemoveMount(host, remotePath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	newMounts := []Mount{}
	for _, m := range r.data.Mounts {
		if m.Host == host && m.RemotePath == remotePath {
			continue
		}
		newMounts = append(newMounts, m)
	}
	r.data.Mounts = newMounts
	return r.save()
}

func (r *Registry) ListMounts() []Mount {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Mount(nil), r.data.Mounts...)
}

func (r *Registry) AddSession(session Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.data.Sessions {
		if s.Host == session.Host && s.SessionName == session.SessionName {
			r.data.Sessions[i] = session
			return r.save()
		}
	}
	r.data.Sessions = append(r.data.Sessions, session)
	return r.save()
}

func (r *Registry) RemoveSession(host, sessionName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	newSessions := []Session{}
	for _, s := range r.data.Sessions {
		if s.Host == host && s.SessionName == sessionName {
			continue
		}
		newSessions = append(newSessions, s)
	}
	r.data.Sessions = newSessions
	return r.save()
}

func (r *Registry) ListSessions() []Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Session(nil), r.data.Sessions...)
}

func (r *Registry) UpdateMountStatus(host, remotePath, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, m := range r.data.Mounts {
		if m.Host == host && m.RemotePath == remotePath {
			r.data.Mounts[i].Status = status
			return r.save()
		}
	}
	return nil
}
