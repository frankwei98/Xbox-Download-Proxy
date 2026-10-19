package offline

import (
	"os"
	"syscall"
)

const ReserveBytes uint64 = 256 * 1024 * 1024

type Storage struct {
	Path           string `json:"path"`
	TotalBytes     uint64 `json:"totalBytes"`
	FreeBytes      uint64 `json:"freeBytes"`
	AvailableBytes uint64 `json:"availableBytes"`
	UsedBytes      uint64 `json:"usedBytes"`
	DownloadBytes  uint64 `json:"downloadBytes"`
	ReserveBytes   uint64 `json:"reserveBytes"`
}

func diskSpace(path string) (Storage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Storage{}, err
	}
	block := uint64(st.Bsize)
	s := Storage{Path: path, TotalBytes: st.Blocks * block, FreeBytes: st.Bfree * block, AvailableBytes: st.Bavail * block, ReserveBytes: ReserveBytes}
	s.UsedBytes = s.TotalBytes - s.FreeBytes
	return s, nil
}

func (m *Manager) Storage() (Storage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.space(m.path)
	if err != nil {
		return s, err
	}
	for _, task := range m.tasks {
		if info, err := m.root.Stat(taskFile(task)); err == nil && info.Mode().IsRegular() {
			s.DownloadBytes += uint64(info.Size())
		} else if err != nil && !os.IsNotExist(err) {
			return s, err
		}
	}
	return s, nil
}
