package offline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) run(ctx context.Context, id string, ctrl *control) {
	defer m.wg.Done()
	err := m.transfer(ctx, id)
	m.mu.Lock()
	task := m.tasks[id]
	task.State, task.SpeedBPS, task.Error = "completed", 0, ""
	if err != nil {
		task.State, task.Error = "failed", err.Error()
		if errors.Is(err, ErrSpace) {
			task.State = "paused"
		}
	}
	if ctrl.desired != "" {
		task.State, task.Error = ctrl.desired, ""
	}
	task.UpdatedAt = time.Now().UTC()
	if err := m.saveLocked(); err != nil {
		task.State, task.Error = "failed", "save task state: "+err.Error()
	}
	ctrl.cancel()
	delete(m.active, id)
	close(ctrl.done)
	m.scheduleLocked()
	m.mu.Unlock()
}

func (m *Manager) transfer(ctx context.Context, id string) (result error) {
	m.mu.Lock()
	task := *m.tasks[id]
	m.mu.Unlock()
	f, err := m.root.OpenFile(taskFile(&task), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Sync(); result == nil && err != nil {
			result = err
		}
		if err := f.Close(); result == nil && err != nil {
			result = err
		}
	}()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("download target must be a regular file")
	}
	offset := info.Size()
	validator := task.ETag
	if strings.HasPrefix(validator, "W/") {
		validator = ""
	}
	if validator == "" {
		validator = task.LastModified
	}
	// Without a validator, appending could splice together different resources.
	if validator == "" {
		offset = 0
	}
	transport := m.transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many download redirects")
		}
		return validateURL(req.URL.String())
	}}
	var response *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, task.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			req.Header.Set("If-Range", validator)
		}
		response, err = client.Do(req)
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
			response.Body.Close()
			offset = 0
			continue
		}
		break
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download source returned HTTP %d; signed URLs may have expired", response.StatusCode)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return errors.New("compressed download response cannot be resumed safely")
	}
	total := response.ContentLength
	if response.StatusCode == http.StatusPartialContent {
		start, end, size, err := parseRange(response.Header.Get("Content-Range"))
		if err != nil || offset == 0 || start != offset || (response.ContentLength >= 0 && response.ContentLength != end-start+1) {
			return errors.New("download source returned an invalid Content-Range")
		}
		if (task.ETag != "" && response.Header.Get("ETag") != "" && task.ETag != response.Header.Get("ETag")) ||
			(task.ETag == "" && task.LastModified != "" && response.Header.Get("Last-Modified") != "" && task.LastModified != response.Header.Get("Last-Modified")) {
			return errors.New("download resource changed during resume; retry with a new task")
		}
		total = size
	} else {
		offset = 0 // Range ignored or If-Range mismatch: safely replace the partial file.
	}
	space, err := m.space(m.path)
	if err != nil {
		return err
	}
	if space.AvailableBytes <= ReserveBytes || (total >= 0 && uint64(total-offset) > space.AvailableBytes-ReserveBytes) {
		return ErrSpace
	}
	if offset == 0 {
		if err := f.Truncate(0); err != nil {
			return err
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	m.mu.Lock()
	live := m.tasks[id]
	live.Bytes, live.TotalBytes = offset, total
	live.ETag, live.LastModified = response.Header.Get("ETag"), response.Header.Get("Last-Modified")
	// Some valid partial responses omit validators; retain the one used in If-Range.
	if response.StatusCode == http.StatusPartialContent {
		if live.ETag == "" {
			live.ETag = task.ETag
		}
		if live.LastModified == "" {
			live.LastModified = task.LastModified
		}
	}
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	bytes := offset
	lastAt, savedAt, lastBytes := time.Now(), time.Now(), offset
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := response.Body.Read(buf)
		if n > 0 {
			if total >= 0 && bytes+int64(n) > total {
				return errors.New("download body exceeds its declared size")
			}
			m.writeMu.Lock()
			space, err := m.space(m.path)
			if err != nil {
				m.writeMu.Unlock()
				return err
			}
			if space.AvailableBytes <= ReserveBytes+uint64(n) {
				m.writeMu.Unlock()
				return ErrSpace
			}
			written, err := f.Write(buf[:n])
			m.writeMu.Unlock()
			bytes += int64(written)
			now := time.Now()
			m.mu.Lock()
			live.Bytes, live.UpdatedAt = bytes, now.UTC()
			if now.Sub(lastAt) >= 250*time.Millisecond {
				live.SpeedBPS = float64(bytes-lastBytes) / now.Sub(lastAt).Seconds()
				lastAt, lastBytes = now, bytes
			}
			var saveErr error
			if now.Sub(savedAt) >= time.Second {
				saveErr = m.saveLocked()
				savedAt = now
			}
			m.mu.Unlock()
			if err != nil {
				return err
			}
			if saveErr != nil {
				return saveErr
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			if total >= 0 && bytes != total {
				return io.ErrUnexpectedEOF
			}
			m.mu.Lock()
			live.TotalBytes = bytes
			m.mu.Unlock()
			return nil
		}
	}
}

func parseRange(value string) (start, end, total int64, err error) {
	err = errors.New("invalid Content-Range")
	if !strings.HasPrefix(value, "bytes ") {
		return
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes "), "/")
	if len(parts) != 2 {
		return
	}
	bounds := strings.Split(parts[0], "-")
	if len(bounds) != 2 {
		return
	}
	a, e1 := strconv.ParseInt(bounds[0], 10, 64)
	b, e2 := strconv.ParseInt(bounds[1], 10, 64)
	c, e3 := strconv.ParseInt(parts[1], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || a < 0 || b < a || c <= b {
		return
	}
	return a, b, c, nil
}
