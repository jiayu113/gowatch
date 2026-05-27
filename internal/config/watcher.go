package config

import (
	"context"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch 监听 path，debounce 后通过 ch 发送新 *Config
// ctx 取消时自动退出，关 watcher
func Watch(ctx context.Context, path string, ch chan<- *Config) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := w.Add(dir); err != nil {
		w.Close()
		return err
	}

	go func() {
		defer w.Close()
		var debounceTimer *time.Timer
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(200*time.Millisecond, func() {
					cfg, err := LoadFromFile(path)
					if err != nil {
						log.Printf("config: reload failed:%v", err)
						return
					}
					select {
					case ch <- cfg:
					case <-ctx.Done():
					}
				})
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("config: watcher error: %v", err)
			}
		}
	}()
	return nil
}
