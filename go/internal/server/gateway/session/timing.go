package session

import (
	"log/slog"
	"time"
)

type requestTiming struct {
	queue, decode, auth, execute, observer time.Duration
	begin, commit, rollback, encode        time.Duration
	items                                  int
}

func (t *requestTiming) log(path string, total time.Duration, responseBytes int, err error, completed bool) {
	status := "ok"
	if !completed {
		status = "panic"
	} else if err != nil {
		status = "error"
	}
	other := total - t.queue - t.decode - t.auth - t.execute - t.observer - t.begin - t.commit - t.rollback - t.encode
	slog.Info("session request phases", "path", path, "status", status,
		"queue_ms", milliseconds(t.queue), "decode_ms", milliseconds(t.decode),
		"auth_ms", milliseconds(t.auth), "execute_ms", milliseconds(t.execute),
		"observer_ms", milliseconds(t.observer), "transaction_begin_ms", milliseconds(t.begin),
		"commit_ms", milliseconds(t.commit), "rollback_ms", milliseconds(t.rollback),
		"encode_ms", milliseconds(t.encode), "other_ms", milliseconds(other),
		"duration_ms", milliseconds(total), "items", t.items, "response_bytes", responseBytes)
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
