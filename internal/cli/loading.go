package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type launchLoading struct {
	writer    io.Writer
	done      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
	visible   bool
	clearOnce sync.Once
}

func startLaunchLoading(writer io.Writer, interactive bool, enabled bool) *launchLoading {
	loading := &launchLoading{writer: writer, done: make(chan struct{})}
	if !interactive || !enabled {
		return loading
	}
	loading.visible = true
	loading.wg.Add(1)
	go func() {
		defer loading.wg.Done()
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		for frame := 0; ; frame = (frame + 1) % len(frames) {
			fmt.Fprintf(writer, "\r\033[2K\033[2m%s\033[0m", frames[frame])
			select {
			case <-loading.done:
				return
			case <-ticker.C:
			}
		}
	}()
	return loading
}

func (loading *launchLoading) Stop() {
	loading.once.Do(func() {
		close(loading.done)
		loading.wg.Wait()
	})
}

func (loading *launchLoading) Clear() {
	loading.Stop()
	loading.clearOnce.Do(func() {
		if loading.visible {
			fmt.Fprint(loading.writer, "\r\033[2K")
		}
	})
}

func (loading *launchLoading) Fail() {
	loading.Clear()
}
