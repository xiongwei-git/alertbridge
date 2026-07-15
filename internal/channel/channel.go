package channel

import (
	"context"
	"fmt"

	"github.com/xiongwei-git/alertbridge/internal/domain"
)

type Sender interface {
	Send(context.Context, domain.Event) (int, error)
}

type SendError struct {
	Message    string
	StatusCode int
	Retryable  bool
}

func (e *SendError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s (status %d)", e.Message, e.StatusCode)
	}
	return e.Message
}
