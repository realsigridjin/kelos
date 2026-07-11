package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// Health checks whether the resident runtime is accepting local connections.
func Health(socketPath string) error {
	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		return fmt.Errorf("connecting to Session runtime: %w", err)
	}
	return connection.Close()
}

// RunJSONClient bridges the process's stdio to the runtime.
func RunJSONClient(ctx context.Context, socketPath string) error {
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("connecting to Session runtime: %w", err)
	}
	defer connection.Close()

	copyErr := make(chan error, 2)
	go func() {
		_, err := io.Copy(connection, os.Stdin)
		if unix, ok := connection.(*net.UnixConn); ok {
			_ = unix.CloseWrite()
		}
		copyErr <- err
	}()
	go func() {
		_, err := io.Copy(os.Stdout, connection)
		copyErr <- err
	}()

	select {
	case err := <-copyErr:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
