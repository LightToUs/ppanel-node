package node

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func normalizedPresenceMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "observe":
		return "observe"
	case "enforce":
		return "enforce"
	default:
		return "disabled"
	}
}

func presenceLeaseTTL(seconds int) time.Duration {
	if seconds < 30 || seconds > 300 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func (c *Controller) authorizePresence(uid int, ip string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	decision, err := c.apiClient.AcquirePresence(ctx, uid, ip)
	if err != nil {
		return false, err
	}
	if decision.Mode == "disabled" {
		return true, nil
	}
	if decision.Mode != "observe" && decision.Mode != "enforce" {
		return false, fmt.Errorf("server returned unsupported presence mode %q", decision.Mode)
	}
	return decision.Allowed, nil
}
