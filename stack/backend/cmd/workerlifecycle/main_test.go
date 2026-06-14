package main

import (
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	t.Run("scale requires scaling config and broker secret", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "scale")
		t.Setenv("ECS_CLUSTER", "cluster")
		t.Setenv("SCALING_CONFIG_PARAM", "")
		t.Setenv("RABBITMQ_URL_SECRET_ARN", "arn:secret")
		if _, err := loadConfig(); err == nil {
			t.Fatal("expected error when SCALING_CONFIG_PARAM is missing")
		}
	})

	t.Run("scale loads with defaults", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "scale")
		t.Setenv("ECS_CLUSTER", "cluster")
		t.Setenv("SCALING_CONFIG_PARAM", "/param")
		t.Setenv("RABBITMQ_URL_SECRET_ARN", "arn:secret")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.managementPort != 443 {
			t.Fatalf("default management port = %d, want 443", cfg.managementPort)
		}
		if cfg.maxAge != 24*time.Hour {
			t.Fatalf("default max age = %v, want 24h", cfg.maxAge)
		}
	})

	t.Run("deploy requires resource prefix", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "deploy")
		t.Setenv("ECS_CLUSTER", "cluster")
		t.Setenv("RESOURCE_PREFIX", "")
		if _, err := loadConfig(); err == nil {
			t.Fatal("expected error when RESOURCE_PREFIX is missing for deploy")
		}
	})

	t.Run("deploy parses task network lists", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "deploy")
		t.Setenv("ECS_CLUSTER", "cluster")
		t.Setenv("RESOURCE_PREFIX", "truth-in-stream-dev")
		t.Setenv("TASK_SUBNET_IDS", "subnet-a, subnet-b")
		t.Setenv("TASK_SECURITY_GROUP_IDS", "sg-a")
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.taskSubnets) != 2 || cfg.taskSubnets[1] != "subnet-b" {
			t.Fatalf("task subnets = %v", cfg.taskSubnets)
		}
		if len(cfg.taskSecurityGroups) != 1 {
			t.Fatalf("task security groups = %v", cfg.taskSecurityGroups)
		}
	})

	t.Run("unknown handler is rejected", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "bogus")
		t.Setenv("ECS_CLUSTER", "cluster")
		if _, err := loadConfig(); err == nil {
			t.Fatal("expected error for unknown handler")
		}
	})

	t.Run("missing cluster is rejected", func(t *testing.T) {
		t.Setenv("LIFECYCLE_HANDLER", "scale")
		t.Setenv("ECS_CLUSTER", "")
		if _, err := loadConfig(); err == nil {
			t.Fatal("expected error when ECS_CLUSTER is missing")
		}
	})
}
