package health

import (
	"strings"
	"testing"

	"nodepanel/master/internal/store"
)

func TestFormatBreachExplainsAutoLoadThreshold(t *testing.T) {
	a := store.HealthAlert{Metric: "load", Threshold: 0, WindowSec: 60}
	got := formatBreach("soild凤凰城", a, Sample{Cores: 8}, 30.67, 16)
	for _, want := range []string{
		"健康告警｜系统任务负载过高",
		"问题：1 分钟任务队列平均数（运行或等待 I/O）",
		"CPU 核心：8 核",
		"当前：30.67（每核 3.83）",
		"阈值：16（8 核 × 2）",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatBreach() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "说明：") || strings.Contains(got, "建议：") {
		t.Fatalf("formatBreach() contains removed detail lines:\n%s", got)
	}
}

func TestFormatBreachNamesResource(t *testing.T) {
	tests := []struct {
		metric string
		want   string
	}{
		{metric: "cpu", want: "CPU 使用率过高"},
		{metric: "mem", want: "内存使用率过高"},
		{metric: "disk", want: "磁盘空间不足"},
		{metric: "iowait", want: "磁盘 I/O 等待过高"},
		{metric: "swap", want: "Swap 使用率过高"},
	}
	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			a := store.HealthAlert{Metric: tt.metric, Threshold: 90, WindowSec: 300}
			got := formatBreach("node", a, Sample{}, 95, 90)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("formatBreach() missing %q:\n%s", tt.want, got)
			}
		})
	}
}
