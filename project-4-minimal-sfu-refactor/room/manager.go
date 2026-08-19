package room

import "sync"

type Manager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewManager creates an empty room manager.
func NewManager() *Manager {
	return &Manager{
		rooms: make(map[string]*Room),
	}
}

// GetOrCreate returns an existing room or creates a new one.
func (m *Manager) GetOrCreate(roomID string) *Room {
	m.mu.Lock()
	defer m.mu.Unlock()

	if r, exists := m.rooms[roomID]; exists {
		return r
	}

	r := NewRoom(roomID)
	m.rooms[roomID] = r
	return r
}

// Get returns a room by ID, or nil.
func (m *Manager) Get(roomID string) *Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rooms[roomID]
}

// Remove deletes a room (called when last peer leaves).
func (m *Manager) Remove(roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rooms, roomID)
}
