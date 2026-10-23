package hostops

import "testing"

func TestMemoryUsesReclaimableAvailability(t *testing.T) {
	u := memoryUsage("MemTotal: 2048 kB\nMemFree: 100 kB\nMemAvailable: 1024 kB\n")
	if u == nil || u.Total != 2097152 || u.Used != 1048576 || u.Available != 1048576 || u.Percent != 50 {
		t.Fatalf("incorrect memory usage: %+v", u)
	}
	for _, text := range []string{"", "MemTotal: 10 kB", "MemTotal: 10 kB\nMemAvailable: 20 kB"} {
		if memoryUsage(text) != nil {
			t.Fatal("invalid measurement reported as valid")
		}
	}
}

func TestUptimeExcludesAggregateIdleTime(t *testing.T) {
	seconds := uptimeSeconds("90061.75 999999.30\n")
	if seconds == nil || *seconds != 90061.75 {
		t.Fatal("aggregate idle CPU time leaked into uptime")
	}
	for _, text := range []string{"", "NaN 123", "+Inf 0", "-1 0", "unknown"} {
		if uptimeSeconds(text) != nil {
			t.Fatal("invalid uptime accepted")
		}
	}
}
