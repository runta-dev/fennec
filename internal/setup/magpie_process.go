package setup

import (
	"context"
	"fennec/internal/gateway"
)

// RefreshMagpieProcess delegates an executable update to the cloud service
// manager. It never signals a process selected by its command-line text.
func RefreshMagpieProcess(ctx context.Context, home string) error {
	return gateway.Refresh(ctx, home)
}
