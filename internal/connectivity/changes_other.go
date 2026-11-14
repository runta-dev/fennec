//go:build !darwin

package connectivity

import "context"

func watchRouteChanges(context.Context) <-chan struct{} { return nil }
