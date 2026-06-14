package workerlifecycle

import (
	"encoding/json"
	"fmt"
	"time"
)

// scalingConfigJSON is the Parameter Store wire shape of one service's scaling
// policy. Cooldown is carried in seconds because JSON has no duration type.
type scalingConfigJSON struct {
	QueueBase       string `json:"queue_base"`
	Ratio           int    `json:"ratio"`
	Min             int    `json:"min"`
	Max             int    `json:"max"`
	CooldownSeconds int    `json:"cooldown_seconds"`
}

// ParseScalingConfig decodes the per-service scaling config JSON (an object
// mapping ECS service name to policy) read from Parameter Store into
// ServiceScaling values. The config lives in Parameter Store rather than a lambda
// env var because the full per-service map can exceed the 4 KiB env limit. An
// empty or "{}" payload yields an empty map - no service scales - which is how the
// fleet stays off until the workers move onto ECS. Each policy is validated:
// QueueBase non-empty, Ratio > 0, Min >= 0, Max >= Min, and CooldownSeconds >= 0.
// Max == 0 (service disabled) is allowed and skips the Max >= Min check.
func ParseScalingConfig(raw []byte) (map[string]ServiceScaling, error) {
	if len(raw) == 0 {
		return map[string]ServiceScaling{}, nil
	}
	var wire map[string]scalingConfigJSON
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("workerlifecycle: parse scaling config: %w", err)
	}
	out := make(map[string]ServiceScaling, len(wire))
	for service, c := range wire {
		if c.QueueBase == "" {
			return nil, fmt.Errorf("workerlifecycle: scaling config for %q has no queue_base", service)
		}
		if c.Ratio <= 0 {
			return nil, fmt.Errorf("workerlifecycle: scaling config for %q has non-positive ratio %d", service, c.Ratio)
		}
		if c.Min < 0 {
			return nil, fmt.Errorf("workerlifecycle: scaling config for %q has negative min %d", service, c.Min)
		}
		if c.CooldownSeconds < 0 {
			return nil, fmt.Errorf("workerlifecycle: scaling config for %q has negative cooldown %d", service, c.CooldownSeconds)
		}
		if c.Max > 0 && c.Max < c.Min {
			return nil, fmt.Errorf("workerlifecycle: scaling config for %q has max %d below min %d", service, c.Max, c.Min)
		}
		out[service] = ServiceScaling{
			QueueBase: c.QueueBase,
			Ratio:     c.Ratio,
			Min:       c.Min,
			Max:       c.Max,
			Cooldown:  time.Duration(c.CooldownSeconds) * time.Second,
		}
	}
	return out, nil
}
